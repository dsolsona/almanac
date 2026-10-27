package storage

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrAgentOwnershipInput     = errors.New("storage: invalid agent ownership input")
	ErrAgentOwnershipDenied    = errors.New("storage: agent ownership denied")
	ErrAgentOwnershipConflict  = errors.New("storage: agent ownership conflict")
	ErrAgentOwnershipExhausted = errors.New("storage: agent ownership counter exhausted")
)

type AgentOwnershipResult struct {
	AgentID         string
	Generation      int64
	RenewalSequence int64
	AcquiredAt      time.Time
	RenewedAt       time.Time
	IssuedAt        time.Time
	ExpiresAt       time.Time
	Nonce           []byte
	ReplaceAt       time.Time
}

type agentOwnershipTransaction struct {
	conn         *pgx.Conn
	tx           pgx.Tx
	queries      *dbgen.Queries
	agentID      pgtype.UUID
	installation dbgen.LockSessionInstallationRow
	agent        dbgen.LockAgentAccessAgentRow
	credential   dbgen.AlmanacAgentCredential
}

func AcquireAgentOwnership(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, clock func() time.Time) (result AgentOwnershipResult, retErr error) {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || clock == nil {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	defer closeAgentOwnershipTransaction(state)
	latest, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	hasOwner := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership lock failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return AgentOwnershipResult{}, ErrInvalidAgentCredential
	}
	if hasOwner && (now.Before(latest.AcquiredAt.Time) || now.Before(latest.RenewedAt.Time)) {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	if hasOwner && now.Before(latest.ExpiresAt.Time) {
		if subtle.ConstantTimeCompare(latest.ResumeVerifier, resumeVerifier[:]) != 1 {
			result := agentOwnershipResult(latest, nil)
			result.ReplaceAt = latest.ExpiresAt.Time.UTC()
			if err := state.tx.Commit(ctx); err != nil {
				return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership conflict commit failed", err)
			}
			return result, ErrAgentOwnershipConflict
		}
		if err := state.tx.Commit(ctx); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership replay commit failed", err)
		}
		return agentOwnershipResult(latest, nil), nil
	}
	if hasOwner && subtle.ConstantTimeCompare(latest.ResumeVerifier, resumeVerifier[:]) == 1 {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	generation := int64(1)
	if hasOwner {
		if latest.Generation == math.MaxInt64 {
			return AgentOwnershipResult{}, ErrAgentOwnershipExhausted
		}
		generation = latest.Generation + 1
	}
	expiresAt := earlierTime(now.Add(time.Hour), effectiveAgentCredentialExpiry(state.credential))
	if now.IsZero() || !now.Before(expiresAt) {
		return AgentOwnershipResult{}, ErrInvalidAgentCredential
	}
	if err := state.queries.InsertAgentOwnership(ctx, dbgen.InsertAgentOwnershipParams{
		AgentID: state.agentID, Generation: generation, ResumeVerifier: resumeVerifier[:],
		AcquiredAt: timestamp(now), RenewedAt: timestamp(now), ExpiresAt: timestamp(expiresAt),
	}); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership insert failed", err)
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership commit failed", err)
	}
	return AgentOwnershipResult{AgentID: state.agentID.String(), Generation: generation,
		AcquiredAt: now, RenewedAt: now, ExpiresAt: expiresAt.UTC()}, nil
}

func ResumeAgentOwnership(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation int64, clock func() time.Time) (AgentOwnershipResult, error) {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || clock == nil || generation <= 0 {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	defer closeAgentOwnershipTransaction(state)
	latest, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership lock failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return AgentOwnershipResult{}, ErrInvalidAgentCredential
	}
	if now.Before(latest.AcquiredAt.Time) || now.Before(latest.RenewedAt.Time) {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	if latest.Generation != generation || !now.Before(latest.ExpiresAt.Time) ||
		subtle.ConstantTimeCompare(latest.ResumeVerifier, resumeVerifier[:]) != 1 {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership resume commit failed", err)
	}
	return agentOwnershipResult(latest, nil), nil
}

func RenewAgentOwnership(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, expectedSequence int64, nonce [32]byte, clock func() time.Time) (AgentOwnershipResult, error) {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || clock == nil || generation <= 0 || expectedSequence < 0 {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	defer closeAgentOwnershipTransaction(state)
	latest, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership lock failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return AgentOwnershipResult{}, ErrInvalidAgentCredential
	}
	if now.Before(latest.AcquiredAt.Time) || now.Before(latest.RenewedAt.Time) {
		return AgentOwnershipResult{}, ErrAgentOwnershipInput
	}
	if latest.Generation != generation || !now.Before(latest.ExpiresAt.Time) ||
		subtle.ConstantTimeCompare(latest.ResumeVerifier, resumeVerifier[:]) != 1 {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if latest.RenewalSequence > 0 && expectedSequence == latest.RenewalSequence-1 &&
		subtle.ConstantTimeCompare(latest.LastNonce, nonce[:]) == 1 {
		result := agentOwnershipResult(latest, nonce[:])
		result.IssuedAt = latest.LastRenewalIssuedAt.Time.UTC()
		result.ExpiresAt = latest.LastRenewalExpiresAt.Time.UTC()
		if err := state.tx.Commit(ctx); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership replay commit failed", err)
		}
		return result, nil
	}
	if expectedSequence != latest.RenewalSequence || latest.RenewalSequence == math.MaxInt64 ||
		(len(latest.LastNonce) > 0 && subtle.ConstantTimeCompare(latest.LastNonce, nonce[:]) == 1) {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	newExpiry := earlierTime(now.Add(time.Hour), effectiveAgentCredentialExpiry(state.credential))
	if !now.Before(newExpiry) || newExpiry.Before(latest.ExpiresAt.Time) {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if err := validateOwnershipChronology(now, latest.AcquiredAt.Time, now, newExpiry); err != nil {
		return AgentOwnershipResult{}, err
	}
	nextSequence := latest.RenewalSequence + 1
	affected, err := state.queries.RenewAgentOwnership(ctx, dbgen.RenewAgentOwnershipParams{
		RenewedAt: timestamp(now), ExpiresAt: timestamp(newExpiry), RenewalSequence: nextSequence,
		LastNonce: nonce[:], LastRenewalIssuedAt: timestamp(now), LastRenewalExpiresAt: timestamp(newExpiry),
		AgentID: state.agentID, Generation: generation,
	})
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership renewal failed", err)
	}
	if affected != 1 {
		return AgentOwnershipResult{}, ErrAgentOwnershipDenied
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership renewal commit failed", err)
	}
	return AgentOwnershipResult{AgentID: state.agentID.String(), Generation: generation,
		RenewalSequence: nextSequence, AcquiredAt: latest.AcquiredAt.Time.UTC(),
		RenewedAt: now, IssuedAt: now, ExpiresAt: newExpiry.UTC(), Nonce: append([]byte(nil), nonce[:]...)}, nil
}

func beginAgentOwnershipTransaction(ctx context.Context, databaseURL string, credentialUUID pgtype.UUID, verifier [32]byte) (*agentOwnershipTransaction, error) {
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		closeInitialOwnerConnection(conn)
	}
	queries := dbgen.New(conn)
	agentID, err := queries.FindAgentAccessCredentialAgent(ctx, credentialUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		cleanup()
		return nil, ErrInvalidAgentCredential
	}
	if err != nil {
		cleanup()
		return nil, operationError(ctx, "storage: agent credential lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		cleanup()
		return nil, operationError(ctx, "storage: agent ownership transaction failed", err)
	}
	fail := func(message string, cause error) (*agentOwnershipTransaction, error) {
		rollbackTransaction(tx)
		cleanup()
		return nil, operationError(ctx, message, cause)
	}
	if err := setTransactionBounds(ctx, tx); err != nil {
		return fail("storage: agent ownership transaction setup failed", err)
	}
	locked := dbgen.New(tx)
	installation, err := locked.LockSessionInstallation(ctx)
	if err != nil {
		return fail("storage: agent ownership installation lock failed", err)
	}
	agent, err := locked.LockAgentAccessAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		rollbackTransaction(tx)
		cleanup()
		return nil, ErrInvalidAgentCredential
	}
	if err != nil {
		return fail("storage: agent ownership agent lock failed", err)
	}
	credential, err := locked.LockAgentAccessCredential(ctx, dbgen.LockAgentAccessCredentialParams{CredentialID: credentialUUID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		rollbackTransaction(tx)
		cleanup()
		return nil, ErrInvalidAgentCredential
	}
	if err != nil {
		return fail("storage: agent ownership credential lock failed", err)
	}
	if len(credential.Verifier) != len(verifier) || subtle.ConstantTimeCompare(credential.Verifier, verifier[:]) != 1 {
		rollbackTransaction(tx)
		cleanup()
		return nil, ErrInvalidAgentCredential
	}
	return &agentOwnershipTransaction{conn: conn, tx: tx, queries: locked, agentID: agentID,
		installation: installation, agent: agent, credential: credential}, nil
}

func closeAgentOwnershipTransaction(state *agentOwnershipTransaction) {
	rollbackTransaction(state.tx)
	closeInitialOwnerConnection(state.conn)
}

func agentOwnershipResult(owner dbgen.AlmanacAgentOwnership, nonce []byte) AgentOwnershipResult {
	result := AgentOwnershipResult{AgentID: owner.AgentID.String(), Generation: owner.Generation,
		RenewalSequence: owner.RenewalSequence, AcquiredAt: owner.AcquiredAt.Time.UTC(),
		RenewedAt: owner.RenewedAt.Time.UTC(), ExpiresAt: owner.ExpiresAt.Time.UTC()}
	if len(nonce) > 0 {
		result.Nonce = append([]byte(nil), nonce...)
	}
	return result
}

func validateOwnershipChronology(now, acquiredAt, renewedAt, expiresAt time.Time) error {
	if now.IsZero() || acquiredAt.IsZero() || renewedAt.IsZero() || expiresAt.IsZero() ||
		acquiredAt.After(renewedAt) || renewedAt.After(now) || !now.Before(expiresAt) {
		return ErrAgentOwnershipInput
	}
	return nil
}

func earlierTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
