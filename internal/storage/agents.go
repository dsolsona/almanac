package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrAgentForbidden    = errors.New("storage: agent operation forbidden")
	ErrInvalidAgentInput = errors.New("storage: invalid agent input")
	ErrAgentConflict     = errors.New("storage: agent conflict")
)

const (
	agentCreateOperation  = "agent.create"
	agentCreateReceiptTTL = 24 * time.Hour
	agentDefaultLifetime  = 90 * 24 * time.Hour
	agentMaxLifetime      = 365 * 24 * time.Hour
)

type AgentAdminProof struct {
	SessionVerifier    [32]byte
	UserID             string
	CredentialEpoch    string
	CredentialRevision int64
	VerifiedAt         time.Time
}

type CreateAgentInput struct {
	Proof              AgentAdminProof
	Name               string
	DisplayName        string
	Lifetime           time.Duration
	CredentialID       string
	CredentialVerifier [32]byte
	IdempotencyKey     string
	RequestID          string
	Now                func() time.Time
}

type AgentRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Enabled     bool      `json:"enabled"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AgentCredentialRecord struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AgentCreation struct {
	Agent      AgentRecord           `json:"agent"`
	Credential AgentCredentialRecord `json:"credential"`
	Replayed   bool                  `json:"replayed"`
}

type agentReceiptResult struct {
	Version    int                   `json:"version"`
	Agent      AgentRecord           `json:"agent"`
	Credential AgentCredentialRecord `json:"credential"`
}

type agentRequestDigest struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	LifetimeSec int64  `json:"lifetime_seconds"`
}

func CreateAgent(ctx context.Context, databaseURL string, input CreateAgentInput) (AgentCreation, error) {
	lifetime, userID, epoch, err := validateCreateAgentInput(input)
	if err != nil {
		return AgentCreation{}, err
	}
	digestBytes, err := json.Marshal(agentRequestDigest{Version: 1, Name: input.Name, DisplayName: input.DisplayName, LifetimeSec: int64(lifetime / time.Second)})
	if err != nil {
		return AgentCreation{}, ErrInvalidAgentInput
	}
	digest := sha256.Sum256(digestBytes)

	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return AgentCreation{}, err
	}
	defer closeInitialOwnerConnection(conn)
	lookup := dbgen.New(conn)
	userIDFromSession, err := lookup.FindAgentSessionUserID(ctx, input.Proof.SessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentCreation{}, ErrInvalidSession
	}
	if err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent session lookup failed", err)
	}
	if userIDFromSession.String() != userID.String() {
		return AgentCreation{}, ErrInvalidSession
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent registration transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent registration setup failed", err)
	}
	queries := dbgen.New(tx)
	installation, err := queries.LockSessionInstallation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentCreation{}, ErrInvalidSession
	}
	if err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent installation lock failed", err)
	}
	user, err := queries.LockSessionUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentCreation{}, ErrInvalidSession
	}
	if err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent user lock failed", err)
	}
	session, err := queries.LockAgentSession(ctx, input.Proof.SessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentCreation{}, ErrInvalidSession
	}
	if err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent session lock failed", err)
	}
	now := input.Now()
	if !validAgentClock(now) {
		return AgentCreation{}, ErrInvalidAgentInput
	}
	now = now.UTC().Truncate(time.Microsecond)
	if invalidSessionState(installation, user, session, now) || session.UserID != userID ||
		user.Role != "administrator" || session.Restricted || user.MustChangePassword || !installation.OnboardingCompletedAt.Valid {
		if invalidSessionState(installation, user, session, now) || session.UserID != userID {
			return AgentCreation{}, ErrInvalidSession
		}
		return AgentCreation{}, ErrAgentForbidden
	}
	if installation.CredentialEpoch.String() != epoch.String() || user.CredentialRevision != input.Proof.CredentialRevision {
		return AgentCreation{}, ErrInvalidSession
	}
	actorID := userID
	receipt, err := queries.FindAgentReceipt(ctx, dbgen.FindAgentReceiptParams{ActorID: actorID, IdempotencyKey: input.IdempotencyKey})
	if err == nil {
		if !now.Before(receipt.ExpiresAt.Time) {
			if _, err := queries.DeleteExpiredAgentReceipt(ctx, dbgen.DeleteExpiredAgentReceiptParams{ActorID: actorID, IdempotencyKey: input.IdempotencyKey, Now: timestamp(now)}); err != nil {
				return AgentCreation{}, operationError(ctx, "storage: agent receipt cleanup failed", err)
			}
		} else {
			if !bytes.Equal(receipt.Digest, digest[:]) {
				return AgentCreation{}, ErrAgentConflict
			}
			saved, decodeErr := decodeAgentCreationReceipt(receipt.Result)
			if decodeErr != nil || !validAgentCreationReceipt(saved) {
				return AgentCreation{}, errors.New("storage: invalid agent receipt")
			}
			if err := tx.Commit(ctx); err != nil {
				return AgentCreation{}, operationError(ctx, "storage: agent receipt commit failed", err)
			}
			return AgentCreation{Agent: saved.Agent, Credential: saved.Credential, Replayed: true}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return AgentCreation{}, operationError(ctx, "storage: agent receipt lookup failed", err)
	}

	agent, err := queries.InsertAgent(ctx, dbgen.InsertAgentParams{Name: input.Name, DisplayName: input.DisplayName, CreatedAt: timestamp(now)})
	if err != nil {
		return AgentCreation{}, agentWriteError(ctx, err, "storage: agent insert failed")
	}
	credentialID, _ := canonicalUUID(input.CredentialID)
	credential, err := queries.InsertAgentCredential(ctx, dbgen.InsertAgentCredentialParams{
		ID: credentialID, AgentID: agent.ID, Verifier: input.CredentialVerifier[:], CredentialEpoch: epoch,
		IssuedAt: timestamp(now), ExpiresAt: timestamp(now.Add(lifetime)),
	})
	if err != nil {
		return AgentCreation{}, agentWriteError(ctx, err, "storage: agent credential insert failed")
	}
	if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: userID.String(), SubjectID: agent.ID, Action: "agent.created", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent audit insert failed", err)
	}
	if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: userID.String(), SubjectID: credential.ID, Action: "agent.credential.issued", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent credential audit insert failed", err)
	}
	created := AgentCreation{
		Agent:      AgentRecord{ID: agent.ID.String(), Name: agent.Name, DisplayName: agent.DisplayName, Enabled: agent.Enabled, Version: agent.Version, CreatedAt: agent.CreatedAt.Time.UTC(), UpdatedAt: agent.UpdatedAt.Time.UTC()},
		Credential: AgentCredentialRecord{ID: credential.ID.String(), AgentID: credential.AgentID.String(), IssuedAt: credential.IssuedAt.Time.UTC(), ExpiresAt: credential.ExpiresAt.Time.UTC()},
	}
	safeResult, err := json.Marshal(agentReceiptResult{Version: 1, Agent: created.Agent, Credential: created.Credential})
	if err != nil {
		return AgentCreation{}, errors.New("storage: agent receipt encoding failed")
	}
	completionTime := input.Now()
	if !validAgentClock(completionTime) {
		return AgentCreation{}, ErrInvalidAgentInput
	}
	completionTime = completionTime.UTC().Truncate(time.Microsecond)
	if completionTime.Before(now) || invalidSessionState(installation, user, session, completionTime) {
		return AgentCreation{}, ErrInvalidSession
	}
	if err := queries.InsertAgentReceipt(ctx, dbgen.InsertAgentReceiptParams{
		ActorID: actorID, IdempotencyKey: input.IdempotencyKey, Digest: digest[:], Result: safeResult,
		CreatedAt: timestamp(completionTime), ExpiresAt: timestamp(completionTime.Add(agentCreateReceiptTTL)),
	}); err != nil {
		return AgentCreation{}, agentWriteError(ctx, err, "storage: agent receipt insert failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentCreation{}, operationError(ctx, "storage: agent registration commit failed", err)
	}
	return created, nil
}

func validAgentCreationReceipt(saved agentReceiptResult) bool {
	if saved.Version != 1 {
		return false
	}
	agentID, ok := canonicalUUID(saved.Agent.ID)
	if !ok || !validAgentRecord(saved.Agent, agentID.String()) || !saved.Agent.Enabled || saved.Agent.Version != 1 || !saved.Agent.CreatedAt.Equal(saved.Agent.UpdatedAt) {
		return false
	}
	credentialID, ok := canonicalUUID(saved.Credential.ID)
	if !ok || saved.Credential.AgentID != agentID.String() || !saved.Credential.IssuedAt.Equal(saved.Agent.CreatedAt) ||
		saved.Credential.ExpiresAt.IsZero() || !saved.Credential.ExpiresAt.After(saved.Credential.IssuedAt) {
		return false
	}
	return credentialID.Valid
}

func validateCreateAgentInput(input CreateAgentInput) (time.Duration, pgtype.UUID, pgtype.UUID, error) {
	lifetime := input.Lifetime
	if lifetime == 0 {
		lifetime = agentDefaultLifetime
	}
	if !validAgentName(input.Name) || !utf8.ValidString(input.DisplayName) || len(input.DisplayName) < 1 || len(input.DisplayName) > 256 ||
		strings.IndexFunc(input.DisplayName, unicode.IsControl) >= 0 || !validPrintableASCII(input.IdempotencyKey, 1, 128) ||
		!validRequestID(input.RequestID) || input.Now == nil || lifetime <= 0 || lifetime > agentMaxLifetime || lifetime%time.Second != 0 ||
		input.Proof.CredentialRevision <= 0 {
		return 0, pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	userID, ok := canonicalUUID(input.Proof.UserID)
	if !ok {
		return 0, pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	epoch, ok := canonicalUUID(input.Proof.CredentialEpoch)
	if !ok {
		return 0, pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	if _, ok := canonicalUUID(input.CredentialID); !ok || input.Proof.VerifiedAt.IsZero() {
		return 0, pgtype.UUID{}, pgtype.UUID{}, ErrInvalidAgentInput
	}
	return lifetime, userID, epoch, nil
}

func validAgentName(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if (value[i] < 'a' || value[i] > 'z') && (value[i] < '0' || value[i] > '9') && value[i] != '-' {
			return false
		}
	}
	return true
}

func validPrintableASCII(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for i := range value {
		if value[i] < 33 || value[i] > 126 {
			return false
		}
	}
	return true
}

func validAgentClock(value time.Time) bool {
	return !value.IsZero()
}

func agentWriteError(ctx context.Context, err error, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return operationError(ctx, message, err)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return ErrAgentConflict
	}
	return operationError(ctx, message, err)
}
