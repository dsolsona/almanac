package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type RevokeAgentCredentialInput struct {
	Proof                     AgentAdminProof
	AgentID, CredentialID     string
	ExpectedVersion           int64
	IdempotencyKey, RequestID string
	Now                       func() time.Time
}
type DisableAgentInput struct {
	Proof                     AgentAdminProof
	AgentID                   string
	ExpectedVersion           int64
	IdempotencyKey, RequestID string
	Now                       func() time.Time
}
type AgentLifecycleResult struct {
	Agent                  AgentRecord                 `json:"agent"`
	Credential             *AgentCredentialStateRecord `json:"credential,omitempty"`
	Changed                bool                        `json:"changed"`
	RevokedCredentialCount int64                       `json:"revoked_credential_count"`
	Replayed               bool                        `json:"replayed"`
}
type agentLifecycleReceipt struct {
	Version                int                         `json:"version"`
	Agent                  AgentRecord                 `json:"agent"`
	Credential             *AgentCredentialStateRecord `json:"credential,omitempty"`
	Changed                bool                        `json:"changed"`
	RevokedCredentialCount int64                       `json:"revoked_credential_count"`
}
type agentLifecycleDigest struct {
	Version         int    `json:"version"`
	Operation       string `json:"operation"`
	AgentID         string `json:"agent_id"`
	CredentialID    string `json:"credential_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type agentRecordReceiptWire struct {
	ID          *string    `json:"id"`
	Name        *string    `json:"name"`
	DisplayName *string    `json:"display_name"`
	Enabled     *bool      `json:"enabled"`
	Version     *int64     `json:"version"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}
type agentCreationCredentialReceiptWire struct {
	ID        *string    `json:"id"`
	AgentID   *string    `json:"agent_id"`
	IssuedAt  *time.Time `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type agentCredentialStateReceiptWire struct {
	ID        *string    `json:"id"`
	AgentID   *string    `json:"agent_id"`
	IssuedAt  *time.Time `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}
type agentCreationReceiptWire struct {
	Version    *int                                `json:"version"`
	Agent      *agentRecordReceiptWire             `json:"agent"`
	Credential *agentCreationCredentialReceiptWire `json:"credential"`
}
type agentLifecycleReceiptWire struct {
	Version                *int                             `json:"version"`
	Agent                  *agentRecordReceiptWire          `json:"agent"`
	Credential             *agentCredentialStateReceiptWire `json:"credential"`
	Changed                *bool                            `json:"changed"`
	RevokedCredentialCount *int64                           `json:"revoked_credential_count"`
}

func RevokeAgentCredential(ctx context.Context, databaseURL string, input RevokeAgentCredentialInput) (AgentLifecycleResult, error) {
	agentID, credentialID, err := validateLifecycleInput(input.Proof, input.AgentID, input.ExpectedVersion, input.IdempotencyKey, input.RequestID, input.Now)
	if err != nil {
		return AgentLifecycleResult{}, err
	}
	credentialID, ok := canonicalUUID(input.CredentialID)
	if !ok {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	return runAgentLifecycle(ctx, databaseURL, input.Proof, agentID, credentialID, "agent.credential.revoke", input.ExpectedVersion, input.IdempotencyKey, input.RequestID, input.Now)
}
func DisableAgent(ctx context.Context, databaseURL string, input DisableAgentInput) (AgentLifecycleResult, error) {
	agentID, _, err := validateLifecycleInput(input.Proof, input.AgentID, input.ExpectedVersion, input.IdempotencyKey, input.RequestID, input.Now)
	if err != nil {
		return AgentLifecycleResult{}, err
	}
	return runAgentLifecycle(ctx, databaseURL, input.Proof, agentID, pgtype.UUID{}, "agent.disable", input.ExpectedVersion, input.IdempotencyKey, input.RequestID, input.Now)
}
func validateLifecycleInput(proof AgentAdminProof, agentID string, version int64, key, requestID string, clock func() time.Time) (pgtype.UUID, pgtype.UUID, error) {
	a, ok := canonicalUUID(agentID)
	if !ok || version <= 0 || !validPrintableASCII(key, 1, 128) || !validRequestID(requestID) || clock == nil || proof.CredentialRevision <= 0 || proof.VerifiedAt.IsZero() {
		return pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	if _, ok := canonicalUUID(proof.UserID); !ok {
		return pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	if _, ok := canonicalUUID(proof.CredentialEpoch); !ok {
		return pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	return a, pgtype.UUID{}, nil
}

func runAgentLifecycle(ctx context.Context, databaseURL string, proof AgentAdminProof, agentID, credentialID pgtype.UUID, operation string, expected int64, key, requestID string, clock func() time.Time) (AgentLifecycleResult, error) {
	digestBytes, err := json.Marshal(agentLifecycleDigest{Version: 1, Operation: operation, AgentID: agentID.String(), CredentialID: credentialString(credentialID), ExpectedVersion: expected})
	if err != nil {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	digest := sha256.Sum256(digestBytes)
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return AgentLifecycleResult{}, err
	}
	defer closeInitialOwnerConnection(conn)
	q := dbgen.New(conn)
	userID, err := q.FindAgentSessionUserID(ctx, proof.SessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	if err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle session lookup failed", err)
	}
	userUUID, ok := canonicalUUID(proof.UserID)
	if !ok || userID != userUUID {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle setup failed", err)
	}
	queries := dbgen.New(tx)
	installation, err := queries.LockSessionInstallation(ctx)
	if err != nil {
		return AgentLifecycleResult{}, lifecycleAuthorityError(ctx, err)
	}
	user, err := queries.LockSessionUser(ctx, userUUID)
	if err != nil {
		return AgentLifecycleResult{}, lifecycleAuthorityError(ctx, err)
	}
	session, err := queries.LockAgentSession(ctx, proof.SessionVerifier[:])
	if err != nil {
		return AgentLifecycleResult{}, lifecycleAuthorityError(ctx, err)
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	verifiedAt := proof.VerifiedAt.UTC().Truncate(time.Microsecond)
	if invalidSessionState(installation, user, session, now) || session.UserID != userUUID {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	if session.Restricted || user.MustChangePassword || !installation.OnboardingCompletedAt.Valid || user.Role != "administrator" {
		return AgentLifecycleResult{}, ErrAgentForbidden
	}
	if installation.CredentialEpoch.String() != proof.CredentialEpoch || user.CredentialRevision != proof.CredentialRevision {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	if now.Before(verifiedAt) || !now.Before(verifiedAt.Add(30*time.Second)) {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	receipt, err := queries.FindAgentLifecycleReceipt(ctx, dbgen.FindAgentLifecycleReceiptParams{ActorID: userUUID, Operation: operation, IdempotencyKey: key})
	if err == nil {
		if !now.Before(receipt.ExpiresAt.Time) {
			if _, err := queries.DeleteExpiredAgentLifecycleReceipt(ctx, dbgen.DeleteExpiredAgentLifecycleReceiptParams{ActorID: userUUID, Operation: operation, IdempotencyKey: key, Now: timestamp(now)}); err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle expired receipt cleanup failed", err)
			}
		} else {
			if !bytes.Equal(receipt.Digest, digest[:]) {
				return AgentLifecycleResult{}, ErrAgentConflict
			}
			saved, decodeErr := decodeAgentLifecycleReceipt(receipt.Result)
			if decodeErr != nil || !validAgentLifecycleReceipt(saved, operation, agentID, credentialID, expected) {
				return AgentLifecycleResult{}, errors.New("storage: invalid agent lifecycle receipt")
			}
			if err := lifecycleCommitCheck(ctx, tx, clock, now, verifiedAt, installation, user, session); err != nil {
				return AgentLifecycleResult{}, err
			}
			return AgentLifecycleResult{Agent: saved.Agent, Credential: saved.Credential, Changed: saved.Changed, RevokedCredentialCount: saved.RevokedCredentialCount, Replayed: true}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle receipt lookup failed", err)
	}
	agent, err := queries.LockLifecycleAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentLifecycleResult{}, ErrAgentNotFound
	}
	if err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle target lock failed", err)
	}
	var credential dbgen.LockLifecycleCredentialRow
	var unrevokedCount int64
	if operation == "agent.credential.revoke" {
		credential, err = queries.LockLifecycleCredential(ctx, dbgen.LockLifecycleCredentialParams{CredentialID: credentialID, AgentID: agentID})
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentLifecycleResult{}, ErrAgentNotFound
		}
		if err != nil {
			return AgentLifecycleResult{}, operationError(ctx, "storage: agent credential lock failed", err)
		}
	} else {
		unrevokedCount, err = queries.LockUnrevokedAgentCredentials(ctx, agentID)
		if err != nil {
			return AgentLifecycleResult{}, operationError(ctx, "storage: agent credentials lock failed", err)
		}
	}
	now2 := normalizedAgentNow(clock)
	if now2.IsZero() {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	if now2.Before(now) || now2.Before(agent.CreatedAt.Time) || now2.Before(agent.UpdatedAt.Time) {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	if !now2.Before(verifiedAt.Add(30*time.Second)) || invalidSessionState(installation, user, session, now2) {
		return AgentLifecycleResult{}, ErrInvalidSession
	}
	now = now2
	if operation == "agent.credential.revoke" && !credential.RevokedAt.Valid && now.Before(credential.IssuedAt.Time) {
		return AgentLifecycleResult{}, ErrInvalidAgentInput
	}
	if operation == "agent.disable" {
		future, err := queries.HasFutureUnrevokedAgentCredential(ctx, dbgen.HasFutureUnrevokedAgentCredentialParams{AgentID: agentID, Now: timestamp(now)})
		if err != nil {
			return AgentLifecycleResult{}, operationError(ctx, "storage: agent credential clock check failed", err)
		}
		if future {
			return AgentLifecycleResult{}, ErrInvalidAgentInput
		}
	}
	if agent.Version != expected {
		return AgentLifecycleResult{}, ErrAgentConflict
	}
	result := AgentLifecycleResult{Agent: agentRecord(agent)}
	if operation == "agent.credential.revoke" {
		if credential.RevokedAt.Valid {
			result.Credential = ptrCredential(credential)
		} else {
			updated, err := queries.RevokeAgentCredential(ctx, dbgen.RevokeAgentCredentialParams{RevokedAt: timestamp(now), CredentialID: credentialID, AgentID: agentID})
			if err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent credential revoke failed", err)
			}
			if agent.Version == math.MaxInt64 {
				return AgentLifecycleResult{}, ErrAgentConflict
			}
			agent, err = queries.UpdateLifecycleAgent(ctx, dbgen.UpdateLifecycleAgentParams{Enabled: agent.Enabled, UpdatedAt: timestamp(now), AgentID: agentID})
			if err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent version update failed", err)
			}
			result.Agent = agentRecord(agent)
			result.Credential = ptrRevokedCredential(updated)
			result.Changed = true
			result.RevokedCredentialCount = 1
			if err := queries.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userUUID.String(), SubjectID: credentialID, Action: "agent.credential.revoked", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent revoke audit failed", err)
			}
		}
		if !result.Changed {
			if err := queries.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userUUID.String(), SubjectID: credentialID, Action: "agent.credential.revoke.noop", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent revoke no-op audit failed", err)
			}
		}
	} else {
		wasEnabled := agent.Enabled
		changed := agent.Enabled || unrevokedCount > 0
		if changed {
			if agent.Version == math.MaxInt64 {
				return AgentLifecycleResult{}, ErrAgentConflict
			}
			agent, err = queries.DisableAgent(ctx, dbgen.DisableAgentParams{UpdatedAt: timestamp(now), AgentID: agentID})
			if err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent disable failed", err)
			}
			result.Agent = agentRecord(agent)
			result.Changed = true
			count, err := queries.RevokeAndAuditUnrevokedAgentCredentials(ctx, dbgen.RevokeAndAuditUnrevokedAgentCredentialsParams{RevokedAt: timestamp(now), AgentID: agentID, Actor: userUUID.String(), OccurredAt: timestamp(now), RequestID: requestID})
			if err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent bulk revoke/audit failed", err)
			}
			result.RevokedCredentialCount = count
			if wasEnabled {
				if err := queries.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userUUID.String(), SubjectID: agentID, Action: "agent.disabled", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
					return AgentLifecycleResult{}, operationError(ctx, "storage: agent disable audit failed", err)
				}
			}
		} else {
			if err := queries.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userUUID.String(), SubjectID: agentID, Action: "agent.disable.noop", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
				return AgentLifecycleResult{}, operationError(ctx, "storage: agent disable no-op audit failed", err)
			}
		}
	}
	result.Replayed = false
	saved := agentLifecycleReceipt{Version: 1, Agent: result.Agent, Credential: result.Credential, Changed: result.Changed, RevokedCredentialCount: result.RevokedCredentialCount}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return AgentLifecycleResult{}, errors.New("storage: agent lifecycle receipt encode failed")
	}
	completion, err := lifecycleTimeCheck(clock, now, verifiedAt, installation, user, session)
	if err != nil {
		return AgentLifecycleResult{}, err
	}
	if err := queries.InsertAgentLifecycleReceipt(ctx, dbgen.InsertAgentLifecycleReceiptParams{ActorID: userUUID, Operation: operation, IdempotencyKey: key, Digest: digest[:], Result: encoded, CreatedAt: timestamp(completion), ExpiresAt: timestamp(completion.Add(agentCreateReceiptTTL))}); err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle receipt insert failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentLifecycleResult{}, operationError(ctx, "storage: agent lifecycle commit failed", err)
	}
	return result, nil
}

func ptrCredential(c dbgen.LockLifecycleCredentialRow) *AgentCredentialStateRecord {
	v := credentialState(c.ID, c.AgentID, c.IssuedAt, c.ExpiresAt, c.RevokedAt)
	return &v
}
func ptrRevokedCredential(c dbgen.RevokeAgentCredentialRow) *AgentCredentialStateRecord {
	v := credentialState(c.ID, c.AgentID, c.IssuedAt, c.ExpiresAt, c.RevokedAt)
	return &v
}
func credentialString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}
func lifecycleAuthorityError(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidSession
	}
	return operationError(ctx, "storage: agent lifecycle authority lock failed", err)
}
func lifecycleCommitCheck(ctx context.Context, tx pgx.Tx, clock func() time.Time, started, verified time.Time, installation dbgen.LockSessionInstallationRow, user dbgen.LockSessionUserRow, session dbgen.AlmanacSession) error {
	if _, err := lifecycleTimeCheck(clock, started, verified, installation, user, session); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return operationError(ctx, "storage: agent lifecycle replay commit failed", err)
	}
	return nil
}
func lifecycleTimeCheck(clock func() time.Time, started, verified time.Time, installation dbgen.LockSessionInstallationRow, user dbgen.LockSessionUserRow, session dbgen.AlmanacSession) (time.Time, error) {
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return time.Time{}, ErrInvalidAgentInput
	}
	if now.Before(started) {
		return time.Time{}, ErrInvalidAgentInput
	}
	if !now.Before(verified.Add(30*time.Second)) || invalidSessionState(installation, user, session, now) {
		return time.Time{}, ErrInvalidSession
	}
	return now, nil
}

func validAgentLifecycleReceipt(saved agentLifecycleReceipt, operation string, agentID, credentialID pgtype.UUID, expected int64) bool {
	if saved.Version != 1 || !validAgentRecord(saved.Agent, agentID.String()) {
		return false
	}
	if expected == math.MaxInt64 {
		if saved.Agent.Version != expected || saved.Changed {
			return false
		}
	} else if saved.Agent.Version != expected && saved.Agent.Version != expected+1 || saved.Changed != (saved.Agent.Version == expected+1) {
		return false
	}
	if operation == "agent.credential.revoke" {
		if saved.Credential == nil || saved.Credential.AgentID != agentID.String() || saved.Credential.ID != credentialID.String() ||
			saved.Credential.RevokedAt == nil || saved.Credential.IssuedAt.IsZero() || !saved.Credential.ExpiresAt.After(saved.Credential.IssuedAt) ||
			saved.Credential.RevokedAt.Before(saved.Credential.IssuedAt) || saved.RevokedCredentialCount < 0 ||
			saved.Changed && saved.RevokedCredentialCount != 1 || !saved.Changed && saved.RevokedCredentialCount != 0 {
			return false
		}
		return true
	}
	return operation == "agent.disable" && saved.Credential == nil && !saved.Agent.Enabled && saved.RevokedCredentialCount >= 0 &&
		(saved.Changed || saved.RevokedCredentialCount == 0)
}
func validAgentRecord(record AgentRecord, expectedID string) bool {
	return record.ID == expectedID && validAgentName(record.Name) && record.DisplayName != "" && utf8.ValidString(record.DisplayName) &&
		len(record.DisplayName) <= 256 && strings.IndexFunc(record.DisplayName, unicode.IsControl) < 0 && record.Version > 0 &&
		!record.CreatedAt.IsZero() && !record.UpdatedAt.IsZero() && !record.UpdatedAt.Before(record.CreatedAt)
}

func decodeAgentRecordReceipt(w *agentRecordReceiptWire) (AgentRecord, bool) {
	if w == nil || w.ID == nil || w.Name == nil || w.DisplayName == nil || w.Enabled == nil || w.Version == nil || w.CreatedAt == nil || w.UpdatedAt == nil {
		return AgentRecord{}, false
	}
	return AgentRecord{ID: *w.ID, Name: *w.Name, DisplayName: *w.DisplayName, Enabled: *w.Enabled, Version: *w.Version, CreatedAt: *w.CreatedAt, UpdatedAt: *w.UpdatedAt}, true
}
func decodeAgentLifecycleReceipt(raw []byte) (agentLifecycleReceipt, error) {
	var w agentLifecycleReceiptWire
	if json.Unmarshal(raw, &w, json.RejectUnknownMembers(true)) != nil || w.Version == nil || w.Agent == nil || w.Changed == nil || w.RevokedCredentialCount == nil {
		return agentLifecycleReceipt{}, errors.New("storage: invalid agent lifecycle receipt")
	}
	agent, ok := decodeAgentRecordReceipt(w.Agent)
	if !ok {
		return agentLifecycleReceipt{}, errors.New("storage: invalid agent lifecycle receipt")
	}
	saved := agentLifecycleReceipt{Version: *w.Version, Agent: agent, Changed: *w.Changed, RevokedCredentialCount: *w.RevokedCredentialCount}
	if w.Credential != nil {
		c := w.Credential
		if c.ID == nil || c.AgentID == nil || c.IssuedAt == nil || c.ExpiresAt == nil || c.RevokedAt == nil {
			return agentLifecycleReceipt{}, errors.New("storage: invalid agent lifecycle receipt")
		}
		saved.Credential = &AgentCredentialStateRecord{ID: *c.ID, AgentID: *c.AgentID, IssuedAt: *c.IssuedAt, ExpiresAt: *c.ExpiresAt, RevokedAt: c.RevokedAt}
	}
	return saved, nil
}
func decodeAgentCreationReceipt(raw []byte) (agentReceiptResult, error) {
	var w agentCreationReceiptWire
	if json.Unmarshal(raw, &w, json.RejectUnknownMembers(true)) != nil || w.Version == nil || w.Agent == nil || w.Credential == nil {
		return agentReceiptResult{}, errors.New("storage: invalid agent receipt")
	}
	agent, ok := decodeAgentRecordReceipt(w.Agent)
	if !ok {
		return agentReceiptResult{}, errors.New("storage: invalid agent receipt")
	}
	c := w.Credential
	if c.ID == nil || c.AgentID == nil || c.IssuedAt == nil || c.ExpiresAt == nil {
		return agentReceiptResult{}, errors.New("storage: invalid agent receipt")
	}
	return agentReceiptResult{Version: *w.Version, Agent: agent, Credential: AgentCredentialRecord{ID: *c.ID, AgentID: *c.AgentID, IssuedAt: *c.IssuedAt, ExpiresAt: *c.ExpiresAt}}, nil
}
