package storage

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidAgentCredential = errors.New("storage: invalid agent credential")

type AgentAccessPrincipal struct {
	AgentID      string
	CredentialID string
	ExpiresAt    time.Time
}

func AuthenticateAgentCredential(ctx context.Context, databaseURL, credentialID string, verifier [32]byte, clock func() time.Time) (AgentAccessPrincipal, error) {
	credentialUUID, ok := canonicalUUID(credentialID)
	if !ok || clock == nil {
		return AgentAccessPrincipal{}, ErrInvalidAgentCredential
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return AgentAccessPrincipal{}, err
	}
	defer closeInitialOwnerConnection(conn)
	queries := dbgen.New(conn)
	agentID, err := queries.FindAgentAccessCredentialAgent(ctx, credentialUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentAccessPrincipal{}, ErrInvalidAgentCredential
	}
	if err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential transaction setup failed", err)
	}
	lockedQueries := dbgen.New(tx)
	installation, err := lockedQueries.LockSessionInstallation(ctx)
	if err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential installation lock failed", err)
	}
	agent, err := lockedQueries.LockAgentAccessAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentAccessPrincipal{}, ErrInvalidAgentCredential
	}
	if err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential agent lock failed", err)
	}
	credential, err := lockedQueries.LockAgentAccessCredential(ctx, dbgen.LockAgentAccessCredentialParams{CredentialID: credentialUUID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentAccessPrincipal{}, ErrInvalidAgentCredential
	}
	if err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential lock failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(installation, agent.Enabled, credential, verifier, now) {
		return AgentAccessPrincipal{}, ErrInvalidAgentCredential
	}
	expiresAt := effectiveAgentCredentialExpiry(credential)
	if err := tx.Commit(ctx); err != nil {
		return AgentAccessPrincipal{}, operationError(ctx, "storage: agent credential transaction commit failed", err)
	}
	return AgentAccessPrincipal{AgentID: agent.ID.String(), CredentialID: credential.ID.String(), ExpiresAt: expiresAt.UTC()}, nil
}

func currentAgentCredentialValid(installation dbgen.LockSessionInstallationRow, enabled bool, credential dbgen.AlmanacAgentCredential, verifier [32]byte, now time.Time) bool {
	return !now.IsZero() && installation.OnboardingCompletedAt.Valid && enabled &&
		credential.CredentialEpoch == installation.CredentialEpoch && len(credential.Verifier) == len(verifier) &&
		subtle.ConstantTimeCompare(credential.Verifier, verifier[:]) == 1 &&
		!credential.IssuedAt.Time.After(now) && now.Before(credential.ExpiresAt.Time) &&
		!credential.RevokedAt.Valid && (!credential.RetireAt.Valid || now.Before(credential.RetireAt.Time))
}

func effectiveAgentCredentialExpiry(credential dbgen.AlmanacAgentCredential) time.Time {
	expiresAt := credential.ExpiresAt.Time
	if credential.RetireAt.Valid && credential.RetireAt.Time.Before(expiresAt) {
		expiresAt = credential.RetireAt.Time
	}
	return expiresAt.UTC()
}
