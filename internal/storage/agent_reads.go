package storage

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrAgentNotFound = errors.New("storage: agent not found")

type AgentReadInput struct {
	SessionVerifier [32]byte
	AgentID         string
	Now             func() time.Time
}
type ListAgentsInput struct {
	SessionVerifier [32]byte
	Limit           int
	Cursor          string
	Now             func() time.Time
}
type ListAgentCredentialsInput struct {
	SessionVerifier [32]byte
	AgentID         string
	Limit           int
	Cursor          string
	Now             func() time.Time
}
type AgentPage struct {
	Items      []AgentRecord `json:"items"`
	NextCursor string        `json:"next_cursor"`
}
type AgentCredentialStateRecord struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	IssuedAt  time.Time  `json:"issued_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}
type AgentCredentialPage struct {
	Items      []AgentCredentialStateRecord `json:"items"`
	NextCursor string                       `json:"next_cursor"`
}

type agentCursor struct {
	Version int       `json:"version"`
	Kind    string    `json:"kind"`
	Time    time.Time `json:"time"`
	ID      string    `json:"id"`
	AgentID string    `json:"agent_id,omitempty"`
}

func GetAgent(ctx context.Context, databaseURL string, input AgentReadInput) (AgentRecord, error) {
	agentID, ok := canonicalUUID(input.AgentID)
	if !ok || input.Now == nil {
		return AgentRecord{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, now, err := beginAgentRead(ctx, databaseURL, input.SessionVerifier, input.Now)
	if err != nil {
		return AgentRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := queries.GetAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRecord{}, ErrAgentNotFound
	}
	if err != nil {
		return AgentRecord{}, operationError(ctx, "storage: agent read failed", err)
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return AgentRecord{}, err
	}
	return agentRecord(row), nil
}

func ListAgents(ctx context.Context, databaseURL string, input ListAgentsInput) (AgentPage, error) {
	limit, cursor, err := validateAgentPage("agents", "", input.Limit, input.Cursor)
	if err != nil || input.Now == nil {
		return AgentPage{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, now, err := beginAgentRead(ctx, databaseURL, input.SessionVerifier, input.Now)
	if err != nil {
		return AgentPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	rows, err := queries.ListAgents(ctx, dbgen.ListAgentsParams{AfterCreatedAt: timestamp(cursor.Time), AfterID: uuidOrZero(cursor.ID), RowLimit: int32(limit + 1)})
	if err != nil {
		return AgentPage{}, operationError(ctx, "storage: agent list failed", err)
	}
	page := AgentPage{Items: []AgentRecord{}}
	for i, row := range rows {
		if i == limit {
			break
		}
		page.Items = append(page.Items, agentRecord(row))
	}
	if len(rows) > limit {
		last := page.Items[len(page.Items)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "agents", Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return AgentPage{}, err
		}
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return AgentPage{}, err
	}
	return page, nil
}

func ListAgentCredentials(ctx context.Context, databaseURL string, input ListAgentCredentialsInput) (AgentCredentialPage, error) {
	agentID, ok := canonicalUUID(input.AgentID)
	if !ok {
		return AgentCredentialPage{}, ErrInvalidAgentInput
	}
	limit, cursor, err := validateAgentPage("credentials", input.AgentID, input.Limit, input.Cursor)
	if err != nil || input.Now == nil {
		return AgentCredentialPage{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, now, err := beginAgentRead(ctx, databaseURL, input.SessionVerifier, input.Now)
	if err != nil {
		return AgentCredentialPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err := queries.GetAgent(ctx, agentID); errors.Is(err, pgx.ErrNoRows) {
		return AgentCredentialPage{}, ErrAgentNotFound
	} else if err != nil {
		return AgentCredentialPage{}, operationError(ctx, "storage: agent credential history owner read failed", err)
	}
	rows, err := queries.ListAgentCredentials(ctx, dbgen.ListAgentCredentialsParams{AgentID: agentID, AfterIssuedAt: timestamp(cursor.Time), AfterID: uuidOrZero(cursor.ID), RowLimit: int32(limit + 1)})
	if err != nil {
		return AgentCredentialPage{}, operationError(ctx, "storage: agent credential list failed", err)
	}
	page := AgentCredentialPage{Items: []AgentCredentialStateRecord{}}
	for i, row := range rows {
		if i == limit {
			break
		}
		page.Items = append(page.Items, credentialState(row.ID, row.AgentID, row.IssuedAt, row.ExpiresAt, row.RevokedAt))
	}
	if len(rows) > limit {
		last := page.Items[len(page.Items)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "credentials", Time: last.IssuedAt, ID: last.ID, AgentID: input.AgentID})
		if err != nil {
			return AgentCredentialPage{}, err
		}
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return AgentCredentialPage{}, err
	}
	return page, nil
}

func beginAgentRead(ctx context.Context, databaseURL string, verifier [32]byte, clock func() time.Time) (*pgx.Conn, *dbgen.Queries, pgx.Tx, dbgen.LockSessionUserRow, dbgen.LockSessionInstallationRow, dbgen.AlmanacSession, time.Time, error) {
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, err
	}
	queries := dbgen.New(conn)
	userID, err := queries.FindAgentSessionUserID(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, ErrInvalidSession
	}
	if err != nil {
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, operationError(ctx, "storage: agent read session lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, operationError(ctx, "storage: agent read transaction failed", err)
	}
	if err := setTransactionBounds(ctx, tx); err != nil {
		rollbackTransaction(tx)
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, operationError(ctx, "storage: agent read setup failed", err)
	}
	q := dbgen.New(tx)
	fail := func(e error) (*pgx.Conn, *dbgen.Queries, pgx.Tx, dbgen.LockSessionUserRow, dbgen.LockSessionInstallationRow, dbgen.AlmanacSession, time.Time, error) {
		rollbackTransaction(tx)
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, e
	}
	installation, err := q.LockSessionInstallation(ctx)
	if err != nil {
		return fail(readAuthorityError(ctx, err))
	}
	user, err := q.LockSessionUser(ctx, userID)
	if err != nil {
		return fail(readAuthorityError(ctx, err))
	}
	session, err := q.LockAgentSessionRead(ctx, verifier[:])
	if err != nil {
		return fail(readAuthorityError(ctx, err))
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return fail(ErrInvalidAgentInput)
	}
	if invalidSessionState(installation, user, session, now) {
		return fail(ErrInvalidSession)
	}
	if session.Restricted || user.MustChangePassword || !installation.OnboardingCompletedAt.Valid {
		return fail(ErrAgentForbidden)
	}
	if user.Role != "administrator" {
		return fail(ErrAgentForbidden)
	}
	return conn, q, tx, user, installation, session, now, nil
}

func finishAgentRead(ctx context.Context, tx pgx.Tx, clock func() time.Time, started time.Time, user dbgen.LockSessionUserRow, installation dbgen.LockSessionInstallationRow, session dbgen.AlmanacSession) error {
	ended := normalizedAgentNow(clock)
	if ended.IsZero() {
		return ErrInvalidAgentInput
	}
	if ended.Before(started) || invalidSessionState(installation, user, session, ended) {
		return ErrInvalidSession
	}
	if err := tx.Commit(ctx); err != nil {
		return operationError(ctx, "storage: agent read commit failed", err)
	}
	return nil
}
func readAuthorityError(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidSession
	}
	return operationError(ctx, "storage: agent read authority lock failed", err)
}
func normalizedAgentNow(clock func() time.Time) time.Time {
	now := clock()
	if !validAgentClock(now) {
		return time.Time{}
	}
	return now.UTC().Truncate(time.Microsecond)
}
func agentRecord(row dbgen.AlmanacAgent) AgentRecord {
	return AgentRecord{ID: row.ID.String(), Name: row.Name, DisplayName: row.DisplayName, Enabled: row.Enabled, Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}
}
func credentialState(id, agentID pgtype.UUID, issued, expires, revoked pgtype.Timestamptz) AgentCredentialStateRecord {
	r := AgentCredentialStateRecord{ID: id.String(), AgentID: agentID.String(), IssuedAt: issued.Time.UTC(), ExpiresAt: expires.Time.UTC()}
	if revoked.Valid {
		v := revoked.Time.UTC()
		r.RevokedAt = &v
	}
	return r
}
func validateAgentPage(kind, agentID string, limit int, cursor string) (int, agentCursor, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return 0, agentCursor{}, ErrInvalidAgentInput
	}
	if len(cursor) == 0 {
		return limit, agentCursor{Version: 1, Kind: kind, AgentID: agentID}, nil
	}
	if len(cursor) > 512 || !isASCII(cursor) {
		return 0, agentCursor{}, ErrInvalidAgentInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != cursor {
		return 0, agentCursor{}, ErrInvalidAgentInput
	}
	var c agentCursor
	if json.Unmarshal(raw, &c, json.RejectUnknownMembers(true)) != nil || c.Version != 1 || c.Kind != kind || c.AgentID != agentID || c.Time.IsZero() || c.Time.Nanosecond()%1000 != 0 {
		return 0, agentCursor{}, ErrInvalidAgentInput
	}
	if _, ok := canonicalUUID(c.ID); !ok {
		return 0, agentCursor{}, ErrInvalidAgentInput
	}
	return limit, c, nil
}
func encodeAgentCursor(c agentCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("storage: agent cursor encode failed")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 512 {
		return "", ErrInvalidAgentInput
	}
	return encoded, nil
}
func isASCII(s string) bool {
	for i := range s {
		if s[i] > 127 {
			return false
		}
	}
	return true
}
func uuidOrZero(id string) pgtype.UUID {
	if id == "" {
		return pgtype.UUID{Valid: true}
	}
	v, _ := canonicalUUID(id)
	return v
}
