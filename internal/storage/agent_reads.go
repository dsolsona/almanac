package storage

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
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
	RetireAt  *time.Time `json:"retire_at"`
}
type AgentCredentialPage struct {
	Items      []AgentCredentialStateRecord `json:"items"`
	NextCursor string                       `json:"next_cursor"`
}

type AgentRuntimeStatus struct {
	Reachability                 string
	Protocol                     string
	SoftwareVersion              string
	Capabilities                 []agentprotocol.Capability
	LastHeartbeatAt              *time.Time
	DesiredConfigurationRevision int64
	AppliedConfigurationRevision int64
	SchedulerHealth              string
	CollectionProgress           string
	ConfigurationFailureCode     *string
	ConfigurationFailureRevision *int64
	LastAttemptAt                *time.Time
	LastSuccessfulQueryAt        *time.Time
	NextExpectedExecutionAt      *time.Time
	CollectionExplanation        string
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
	record, err := populateAgentRuntimeStatus(ctx, queries, agentRecord(row), now)
	if err != nil {
		return AgentRecord{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return AgentRecord{}, err
	}
	return record, nil
}

func populateAgentRuntimeStatus(ctx context.Context, queries *dbgen.Queries, record AgentRecord, now time.Time) (AgentRecord, error) {
	agentID, valid := canonicalUUID(record.ID)
	if !valid {
		return AgentRecord{}, ErrInvalidAgentInput
	}
	incompatibleAt, incompatibleErr := queries.GetAgentIncompatibleAttempt(ctx, agentID)
	hasIncompatibleAttempt := incompatibleErr == nil
	if incompatibleErr != nil && !errors.Is(incompatibleErr, pgx.ErrNoRows) {
		return AgentRecord{}, operationError(ctx, "storage: incompatible agent state read failed", incompatibleErr)
	}
	runtimeRow, runtimeErr := queries.GetAgentRuntimeStatus(ctx, agentID)
	if runtimeErr == nil {
		record.Runtime, runtimeErr = runtimeStatus(runtimeRow, now)
		if runtimeErr != nil {
			return AgentRecord{}, operationError(ctx, "storage: agent runtime capability state invalid", runtimeErr)
		}
		if hasIncompatibleAttempt {
			activityAt := runtimeRow.UpdatedAt.Time
			if runtimeRow.LastHeartbeatAt.Valid {
				activityAt = runtimeRow.LastHeartbeatAt.Time
			}
			if !incompatibleAt.Time.Before(activityAt) {
				record.Runtime.Reachability = "incompatible"
			}
		}
	} else if !errors.Is(runtimeErr, pgx.ErrNoRows) {
		return AgentRecord{}, operationError(ctx, "storage: agent runtime status read failed", runtimeErr)
	} else if hasIncompatibleAttempt {
		configuration, err := queries.GetAgentConfiguration(ctx, agentID)
		if err != nil {
			return AgentRecord{}, operationError(ctx, "storage: incompatible agent configuration read failed", err)
		}
		record.Runtime = &AgentRuntimeStatus{
			Reachability: "incompatible", Protocol: "incompatible", SoftwareVersion: "unknown",
			Capabilities: []agentprotocol.Capability{}, LastHeartbeatAt: nil,
			DesiredConfigurationRevision: configuration.Revision, AppliedConfigurationRevision: 0,
			SchedulerHealth: "degraded", CollectionProgress: "not_started",
			CollectionExplanation: "no_assignment",
		}
	}
	if record.Runtime != nil {
		assigned, err := queries.AgentHasAssignedDatasources(ctx, agentID)
		if err != nil {
			return AgentRecord{}, operationError(ctx, "storage: agent assignment state read failed", err)
		}
		if assigned {
			record.Runtime.CollectionExplanation = "none"
		}
	}
	return record, nil
}

func runtimeStatus(row dbgen.GetAgentRuntimeStatusRow, now time.Time) (*AgentRuntimeStatus, error) {
	var capabilities []agentprotocol.Capability
	if json.Unmarshal(row.Capabilities, &capabilities) != nil || capabilities == nil {
		return nil, errors.New("storage: agent runtime capabilities invalid")
	}
	for _, capability := range capabilities {
		if strings.TrimSpace(capability.Provider) == "" || capability.ConfigurationFormats == nil || capability.Operations == nil {
			return nil, errors.New("storage: agent runtime capabilities invalid")
		}
	}
	heartbeatAt := nullableTimestamp(row.LastHeartbeatAt)
	activityAt := heartbeatAt
	if activityAt == nil {
		activityAt = nullableTimestamp(row.UpdatedAt)
	}
	reachability := "unreachable"
	if activityAt != nil && !now.After(activityAt.Add(30*time.Second)) {
		reachability = "connected"
	}
	return &AgentRuntimeStatus{
		Reachability: reachability, Protocol: row.Protocol, SoftwareVersion: row.SoftwareVersion,
		Capabilities: capabilities, LastHeartbeatAt: heartbeatAt,
		DesiredConfigurationRevision: row.DesiredConfigurationRevision,
		AppliedConfigurationRevision: row.AppliedConfigurationRevision,
		SchedulerHealth:              row.SchedulerHealth, CollectionProgress: row.CollectionProgress,
		ConfigurationFailureCode:     nullableText(row.ConfigurationFailureCode),
		ConfigurationFailureRevision: nullableInt64(row.ConfigurationFailureRevision),
		LastAttemptAt:                nullableTimestamp(row.LastAttemptAt),
		LastSuccessfulQueryAt:        nullableTimestamp(row.LastSuccessfulQueryAt),
		NextExpectedExecutionAt:      nullableTimestamp(row.NextExpectedExecutionAt),
		CollectionExplanation:        "no_assignment",
	}, nil
}

func nullableText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableTimestamp(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
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
		record, err := populateAgentRuntimeStatus(ctx, queries, agentRecord(row), now)
		if err != nil {
			return AgentPage{}, err
		}
		page.Items = append(page.Items, record)
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
		page.Items = append(page.Items, credentialState(row.ID, row.AgentID, row.IssuedAt, row.ExpiresAt, row.RevokedAt, row.RetireAt))
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
	return beginAgentReadWithRoles(ctx, databaseURL, verifier, clock, "administrator")
}

func beginAgentReadWithRoles(ctx context.Context, databaseURL string, verifier [32]byte, clock func() time.Time, roles ...string) (*pgx.Conn, *dbgen.Queries, pgx.Tx, dbgen.LockSessionUserRow, dbgen.LockSessionInstallationRow, dbgen.AlmanacSession, time.Time, error) {
	return beginAgentReadWithOptions(ctx, databaseURL, verifier, clock, pgx.TxOptions{}, roles...)
}

func beginAgentReadWithOptions(ctx context.Context, databaseURL string, verifier [32]byte, clock func() time.Time, options pgx.TxOptions, roles ...string) (*pgx.Conn, *dbgen.Queries, pgx.Tx, dbgen.LockSessionUserRow, dbgen.LockSessionInstallationRow, dbgen.AlmanacSession, time.Time, error) {
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
	tx, err := conn.BeginTx(ctx, options)
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
	if !slices.Contains(roles, user.Role) {
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
		return readTransactionError(ctx, "storage: agent read commit failed", err)
	}
	return nil
}
func readAuthorityError(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidSession
	}
	return readTransactionError(ctx, "storage: agent read authority lock failed", err)
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
func credentialState(id, agentID pgtype.UUID, issued, expires, revoked, retire pgtype.Timestamptz) AgentCredentialStateRecord {
	r := AgentCredentialStateRecord{ID: id.String(), AgentID: agentID.String(), IssuedAt: issued.Time.UTC(), ExpiresAt: expires.Time.UTC()}
	if revoked.Valid {
		v := revoked.Time.UTC()
		r.RevokedAt = &v
	}
	if retire.Valid {
		v := retire.Time.UTC()
		r.RetireAt = &v
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
