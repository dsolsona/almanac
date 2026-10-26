package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const agentRotationOperation = "agent.credential.rotate"

type RotateAgentCredentialInput struct {
	Proof                     AgentAdminProof
	AgentID                   string
	ExpectedVersion           int64
	Lifetime                  time.Duration
	Overlap                   *time.Duration
	CredentialID              string
	CredentialVerifier        [32]byte
	IdempotencyKey, RequestID string
	Now                       func() time.Time
}

type AgentRotationResult struct {
	Agent              AgentRecord                 `json:"agent"`
	Credential         AgentCredentialStateRecord  `json:"credential"`
	PreviousCredential *AgentCredentialStateRecord `json:"previous_credential,omitempty"`
	Replayed           bool                        `json:"replayed"`
}

type agentRotationDigest struct {
	Version         int    `json:"version"`
	Operation       string `json:"operation"`
	AgentID         string `json:"agent_id"`
	ExpectedVersion int64  `json:"expected_version"`
	LifetimeSeconds int64  `json:"lifetime_seconds"`
	OverlapSeconds  int64  `json:"overlap_seconds"`
}
type agentRotationReceipt struct {
	Version            int                         `json:"version"`
	Agent              AgentRecord                 `json:"agent"`
	Credential         AgentCredentialStateRecord  `json:"credential"`
	PreviousCredential *AgentCredentialStateRecord `json:"previous_credential,omitempty"`
}
type rotationCredentialWire struct {
	ID        *string        `json:"id"`
	AgentID   *string        `json:"agent_id"`
	IssuedAt  *time.Time     `json:"issued_at"`
	ExpiresAt *time.Time     `json:"expires_at"`
	RevokedAt jsontext.Value `json:"revoked_at"`
	RetireAt  jsontext.Value `json:"retire_at"`
}
type rotationReceiptWire struct {
	Version            *int                    `json:"version"`
	Agent              *agentRecordReceiptWire `json:"agent"`
	Credential         *rotationCredentialWire `json:"credential"`
	PreviousCredential *rotationCredentialWire `json:"previous_credential"`
}

func RotateAgentCredential(ctx context.Context, databaseURL string, input RotateAgentCredentialInput) (AgentRotationResult, error) {
	lifetime := input.Lifetime
	if lifetime == 0 {
		lifetime = agentDefaultLifetime
	}
	overlap := 10 * time.Minute
	if input.Overlap != nil {
		overlap = *input.Overlap
	}
	agentID, ok := canonicalUUID(input.AgentID)
	credentialID, credentialOK := canonicalUUID(input.CredentialID)
	userID, userOK := canonicalUUID(input.Proof.UserID)
	epoch, epochOK := canonicalUUID(input.Proof.CredentialEpoch)
	if !ok || !credentialOK || !userOK || !epochOK || input.ExpectedVersion <= 0 ||
		!validPrintableASCII(input.IdempotencyKey, 1, 128) || !validRequestID(input.RequestID) || input.Now == nil ||
		input.Proof.CredentialRevision <= 0 || input.Proof.VerifiedAt.IsZero() ||
		lifetime <= 0 || lifetime > agentMaxLifetime || lifetime%time.Second != 0 ||
		overlap < 0 || overlap > 10*time.Minute || overlap%time.Second != 0 {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if credentialID.String() == "" {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	digestRaw, err := json.Marshal(agentRotationDigest{Version: 1, Operation: agentRotationOperation, AgentID: agentID.String(), ExpectedVersion: input.ExpectedVersion, LifetimeSeconds: int64(lifetime / time.Second), OverlapSeconds: int64(overlap / time.Second)})
	if err != nil {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	digest := sha256.Sum256(digestRaw)
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return AgentRotationResult{}, err
	}
	defer closeInitialOwnerConnection(conn)
	lookup := dbgen.New(conn)
	sessionUser, err := lookup.FindAgentSessionUserID(ctx, input.Proof.SessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRotationResult{}, ErrInvalidSession
	}
	if err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation session lookup failed", err)
	}
	if sessionUser != userID {
		return AgentRotationResult{}, ErrInvalidSession
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation setup failed", err)
	}
	q := dbgen.New(tx)
	installation, err := q.LockSessionInstallation(ctx)
	if err != nil {
		return AgentRotationResult{}, lifecycleAuthorityError(ctx, err)
	}
	user, err := q.LockSessionUser(ctx, userID)
	if err != nil {
		return AgentRotationResult{}, lifecycleAuthorityError(ctx, err)
	}
	session, err := q.LockAgentSession(ctx, input.Proof.SessionVerifier[:])
	if err != nil {
		return AgentRotationResult{}, lifecycleAuthorityError(ctx, err)
	}
	now := normalizedAgentNow(input.Now)
	verifiedAt := input.Proof.VerifiedAt.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if invalidSessionState(installation, user, session, now) || session.UserID != userID {
		return AgentRotationResult{}, ErrInvalidSession
	}
	if session.Restricted || user.MustChangePassword || !installation.OnboardingCompletedAt.Valid || user.Role != "administrator" {
		return AgentRotationResult{}, ErrAgentForbidden
	}
	if installation.CredentialEpoch != epoch || user.CredentialRevision != input.Proof.CredentialRevision {
		return AgentRotationResult{}, ErrInvalidSession
	}
	if now.Before(verifiedAt) || !now.Before(verifiedAt.Add(30*time.Second)) {
		return AgentRotationResult{}, ErrInvalidSession
	}
	receipt, err := q.FindAgentLifecycleReceipt(ctx, dbgen.FindAgentLifecycleReceiptParams{ActorID: userID, Operation: agentRotationOperation, IdempotencyKey: input.IdempotencyKey})
	if err == nil {
		if !now.Before(receipt.ExpiresAt.Time) {
			if _, err := q.DeleteExpiredAgentLifecycleReceipt(ctx, dbgen.DeleteExpiredAgentLifecycleReceiptParams{ActorID: userID, Operation: agentRotationOperation, IdempotencyKey: input.IdempotencyKey, Now: timestamp(now)}); err != nil {
				return AgentRotationResult{}, operationError(ctx, "storage: agent rotation receipt cleanup failed", err)
			}
		} else {
			if !bytes.Equal(receipt.Digest, digest[:]) {
				return AgentRotationResult{}, ErrAgentConflict
			}
			saved, decodeErr := decodeAgentRotationReceipt(receipt.Result)
			if decodeErr != nil || !validAgentRotationReceipt(saved, agentID, input.ExpectedVersion) {
				return AgentRotationResult{}, errors.New("storage: invalid agent rotation receipt")
			}
			if err := lifecycleCommitCheck(ctx, tx, input.Now, now, verifiedAt, installation, user, session); err != nil {
				return AgentRotationResult{}, err
			}
			return AgentRotationResult{Agent: saved.Agent, Credential: saved.Credential, PreviousCredential: saved.PreviousCredential, Replayed: true}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation receipt lookup failed", err)
	}
	agent, err := q.LockLifecycleAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRotationResult{}, ErrAgentNotFound
	}
	if err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation target lock failed", err)
	}
	postAgentLock := normalizedAgentNow(input.Now)
	if postAgentLock.IsZero() {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if postAgentLock.Before(now) {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	now = postAgentLock
	if now.Before(verifiedAt) || now.Before(agent.CreatedAt.Time) || now.Before(agent.UpdatedAt.Time) {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if !now.Before(verifiedAt.Add(30*time.Second)) || invalidSessionState(installation, user, session, now) {
		return AgentRotationResult{}, ErrInvalidSession
	}
	if !agent.Enabled {
		return AgentRotationResult{}, ErrAgentConflict
	}
	if agent.Version != input.ExpectedVersion || agent.Version == math.MaxInt64 {
		return AgentRotationResult{}, ErrAgentConflict
	}
	active, err := q.LockActiveRotationCredentials(ctx, dbgen.LockActiveRotationCredentialsParams{AgentID: agentID, CredentialEpoch: epoch, Now: timestamp(now)})
	if err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: active agent credential lock failed", err)
	}
	// Requery after lock waits so candidates that expired while waiting cannot hide later active rows behind LIMIT 2.
	afterLocks := normalizedAgentNow(input.Now)
	if afterLocks.IsZero() {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if afterLocks.Before(now) {
		return AgentRotationResult{}, ErrInvalidAgentInput
	}
	if !afterLocks.Before(verifiedAt.Add(30*time.Second)) || invalidSessionState(installation, user, session, afterLocks) {
		return AgentRotationResult{}, ErrInvalidSession
	}
	if !afterLocks.Equal(now) {
		now = afterLocks
		active, err = q.LockActiveRotationCredentials(ctx, dbgen.LockActiveRotationCredentialsParams{AgentID: agentID, CredentialEpoch: epoch, Now: timestamp(now)})
		if err != nil {
			return AgentRotationResult{}, operationError(ctx, "storage: active agent credential recheck failed", err)
		}
	}
	if len(active) > 1 {
		return AgentRotationResult{}, ErrAgentConflict
	}
	var previous *AgentCredentialStateRecord
	if len(active) == 1 {
		c := active[0]
		cutoff := now.Add(overlap)
		if c.ExpiresAt.Time.Before(cutoff) {
			cutoff = c.ExpiresAt.Time
		}
		if c.RetireAt.Valid && c.RetireAt.Time.Before(cutoff) {
			cutoff = c.RetireAt.Time
		}
		if cutoff.Before(c.IssuedAt.Time) {
			return AgentRotationResult{}, ErrInvalidAgentInput
		}
		if rows, err := q.RetireAgentCredential(ctx, dbgen.RetireAgentCredentialParams{RetireAt: timestamp(cutoff), ID: c.ID, AgentID: agentID}); err != nil {
			return AgentRotationResult{}, operationError(ctx, "storage: agent credential retirement failed", err)
		} else if rows != 1 {
			return AgentRotationResult{}, ErrAgentConflict
		}
		v := credentialState(c.ID, c.AgentID, c.IssuedAt, c.ExpiresAt, c.RevokedAt, pgtype.Timestamptz{Time: cutoff, Valid: true})
		previous = &v
	}
	newCred, err := q.InsertRotatedAgentCredential(ctx, dbgen.InsertRotatedAgentCredentialParams{ID: credentialID, AgentID: agentID, Verifier: input.CredentialVerifier[:], CredentialEpoch: epoch, IssuedAt: timestamp(now), ExpiresAt: timestamp(now.Add(lifetime))})
	if err != nil {
		return AgentRotationResult{}, agentWriteError(ctx, err, "storage: rotated credential insert failed")
	}
	updated, err := q.UpdateLifecycleAgent(ctx, dbgen.UpdateLifecycleAgentParams{Enabled: true, UpdatedAt: timestamp(now), AgentID: agentID})
	if err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation version update failed", err)
	}
	if err := q.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userID.String(), SubjectID: agentID, Action: "agent.credential.rotated", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation audit failed", err)
	}
	if err := q.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userID.String(), SubjectID: credentialID, Action: "agent.credential.issued", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: rotated credential audit failed", err)
	}
	if previous != nil {
		previousID, _ := canonicalUUID(previous.ID)
		if err := q.InsertLifecycleAudit(ctx, dbgen.InsertLifecycleAuditParams{Actor: userID.String(), SubjectID: previousID, Action: "agent.credential.retirement.scheduled", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
			return AgentRotationResult{}, operationError(ctx, "storage: credential retirement audit failed", err)
		}
	}
	result := AgentRotationResult{Agent: agentRecord(updated), Credential: credentialState(newCred.ID, newCred.AgentID, newCred.IssuedAt, newCred.ExpiresAt, newCred.RevokedAt, newCred.RetireAt), PreviousCredential: previous}
	saved := agentRotationReceipt{Version: 1, Agent: result.Agent, Credential: result.Credential, PreviousCredential: result.PreviousCredential}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return AgentRotationResult{}, errors.New("storage: agent rotation receipt encode failed")
	}
	completion, err := lifecycleTimeCheck(input.Now, now, verifiedAt, installation, user, session)
	if err != nil {
		return AgentRotationResult{}, err
	}
	if err := q.InsertAgentLifecycleReceipt(ctx, dbgen.InsertAgentLifecycleReceiptParams{ActorID: userID, Operation: agentRotationOperation, IdempotencyKey: input.IdempotencyKey, Digest: digest[:], Result: encoded, CreatedAt: timestamp(completion), ExpiresAt: timestamp(completion.Add(agentCreateReceiptTTL))}); err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation receipt insert failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentRotationResult{}, operationError(ctx, "storage: agent rotation commit failed", err)
	}
	return result, nil
}

func decodeAgentRotationCredential(w *rotationCredentialWire) (AgentCredentialStateRecord, bool) {
	if w == nil || w.ID == nil || w.AgentID == nil || w.IssuedAt == nil || w.ExpiresAt == nil {
		return AgentCredentialStateRecord{}, false
	}
	revokedAt, present, err := decodeRotationTime(w.RevokedAt)
	if err != nil || !present || revokedAt != nil {
		return AgentCredentialStateRecord{}, false
	}
	retireAt, present, err := decodeRotationTime(w.RetireAt)
	if err != nil || !present {
		return AgentCredentialStateRecord{}, false
	}
	return AgentCredentialStateRecord{ID: *w.ID, AgentID: *w.AgentID, IssuedAt: *w.IssuedAt, ExpiresAt: *w.ExpiresAt, RevokedAt: revokedAt, RetireAt: retireAt}, true
}

func decodeRotationTime(raw jsontext.Value) (*time.Time, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return nil, true, nil
	}
	var value time.Time
	if err := json.Unmarshal(raw, &value); err != nil || value.IsZero() {
		return nil, true, errors.New("storage: invalid agent rotation receipt time")
	}
	return &value, true, nil
}
func decodeAgentRotationReceipt(raw []byte) (agentRotationReceipt, error) {
	var w rotationReceiptWire
	if json.Unmarshal(raw, &w, json.RejectUnknownMembers(true)) != nil || w.Version == nil || w.Agent == nil || w.Credential == nil {
		return agentRotationReceipt{}, errors.New("storage: invalid agent rotation receipt")
	}
	agent, ok := decodeAgentRecordReceipt(w.Agent)
	if !ok {
		return agentRotationReceipt{}, errors.New("storage: invalid agent rotation receipt")
	}
	cred, ok := decodeAgentRotationCredential(w.Credential)
	if !ok {
		return agentRotationReceipt{}, errors.New("storage: invalid agent rotation receipt")
	}
	saved := agentRotationReceipt{Version: *w.Version, Agent: agent, Credential: cred}
	if w.PreviousCredential != nil {
		previous, ok := decodeAgentRotationCredential(w.PreviousCredential)
		if !ok {
			return agentRotationReceipt{}, errors.New("storage: invalid agent rotation receipt")
		}
		saved.PreviousCredential = &previous
	}
	return saved, nil
}
func validRotationCredential(c AgentCredentialStateRecord, agentID string) bool {
	_, idOK := canonicalUUID(c.ID)
	_, agentOK := canonicalUUID(c.AgentID)
	return idOK && agentOK && c.AgentID == agentID && !c.IssuedAt.IsZero() && c.ExpiresAt.After(c.IssuedAt) && c.RevokedAt == nil && (c.RetireAt == nil || !c.RetireAt.Before(c.IssuedAt))
}
func validAgentRotationReceipt(saved agentRotationReceipt, agentID pgtype.UUID, expected int64) bool {
	if saved.Version != 1 || !validAgentRecord(saved.Agent, agentID.String()) || !saved.Agent.Enabled || saved.Agent.Version != expected+1 || !validRotationCredential(saved.Credential, agentID.String()) {
		return false
	}
	if saved.Credential.RetireAt != nil || saved.PreviousCredential != nil && (!validRotationCredential(*saved.PreviousCredential, agentID.String()) || saved.PreviousCredential.RetireAt == nil || saved.PreviousCredential.ID == saved.Credential.ID) {
		return false
	}
	return true
}
