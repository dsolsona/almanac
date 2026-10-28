package storage

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
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
	AgentID               string
	Generation            int64
	RenewalSequence       int64
	AcquiredAt            time.Time
	RenewedAt             time.Time
	IssuedAt              time.Time
	ExpiresAt             time.Time
	Nonce                 []byte
	ReplaceAt             time.Time
	LeaseID               string
	ConfigurationRevision int64
	ConfigurationSnapshot []byte
	AllowedMeasurements   []byte
	LeaseValidFrom        time.Time
	LeaseValidUntil       time.Time
}

type AgentRuntimeHello struct {
	Protocol        string
	SoftwareVersion string
	Capabilities    []byte
}

type AgentRuntimeHeartbeat struct {
	ReceivedAt                   time.Time
	LeaseID                      string
	AppliedConfigurationRevision int64
	SchedulerHealth              string
	CollectionProgress           string
	LastAttemptAt                *time.Time
	LastSuccessfulQueryAt        *time.Time
	NextExpectedExecutionAt      *time.Time
}

const agentGrantRetention = 7 * 24 * time.Hour
const agentMinimumRenewalInterval = 10 * time.Second

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
	return AcquireAgentOwnershipWithHello(ctx, databaseURL, credentialID, credentialVerifier, resumeVerifier, nil, clock)
}

func AcquireAgentOwnershipWithHello(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, hello *AgentRuntimeHello, clock func() time.Time) (result AgentOwnershipResult, retErr error) {
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
		result, err := ensureAgentRuntimeGrant(ctx, state.queries, latest)
		if err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent runtime state unavailable", err)
		}
		if hello != nil {
			if err := saveAgentRuntimeHello(ctx, state.queries, state.agentID, latest.Generation, *hello, now); err != nil {
				return AgentOwnershipResult{}, operationError(ctx, "storage: agent hello status save failed", err)
			}
		}
		if err := state.tx.Commit(ctx); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership replay commit failed", err)
		}
		return result, nil
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
	created, err := ensureAgentRuntimeGrant(ctx, state.queries, dbgen.AlmanacAgentOwnership{
		AgentID: state.agentID, Generation: generation, AcquiredAt: timestamp(now), RenewedAt: timestamp(now),
		ExpiresAt: timestamp(expiresAt),
	})
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent execution grant insert failed", err)
	}
	if err := state.queries.DeleteExpiredAgentExecutionGrants(ctx, dbgen.DeleteExpiredAgentExecutionGrantsParams{
		AgentID: state.agentID, RetainUntil: timestamp(now),
	}); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: expired agent grant pruning failed", err)
	}
	if hello != nil {
		if err := saveAgentRuntimeHello(ctx, state.queries, state.agentID, generation, *hello, now); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent hello status save failed", err)
		}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership commit failed", err)
	}
	return created, nil
}

// RecordAgentIncompatibleAttempt records only a safe timestamp for a well-formed
// hello that cannot negotiate a supported protocol. It never creates ownership
// or execution authority.
func RecordAgentIncompatibleAttempt(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation int64, clock func() time.Time) (int64, error) {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || generation < 0 || clock == nil {
		return 0, ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return 0, err
	}
	defer closeAgentOwnershipTransaction(state)
	owner, ownerErr := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	hasOwner := ownerErr == nil
	if errors.Is(ownerErr, pgx.ErrNoRows) {
		ownerErr = nil
	}
	if ownerErr != nil {
		return 0, operationError(ctx, "storage: incompatible attempt owner lookup failed", ownerErr)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return 0, ErrInvalidAgentCredential
	}
	if hasOwner && (now.Before(owner.AcquiredAt.Time) || now.Before(owner.RenewedAt.Time)) {
		return 0, ErrAgentOwnershipInput
	}
	if generation > 0 && !hasOwner {
		return 0, ErrAgentOwnershipDenied
	}
	if hasOwner && now.Before(owner.ExpiresAt.Time) {
		if generation > 0 && owner.Generation != generation || subtle.ConstantTimeCompare(owner.ResumeVerifier, resumeVerifier[:]) != 1 {
			return 0, ErrAgentOwnershipConflict
		}
	} else if generation > 0 {
		return 0, ErrAgentOwnershipDenied
	}
	configuration, err := ensureAgentConfiguration(ctx, state.queries, state.agentID, now)
	if err != nil {
		return 0, operationError(ctx, "storage: incompatible attempt configuration unavailable", err)
	}
	if err := state.queries.UpsertAgentIncompatibleAttempt(ctx, dbgen.UpsertAgentIncompatibleAttemptParams{
		AgentID: state.agentID, AttemptedAt: timestamp(now),
	}); err != nil {
		return 0, operationError(ctx, "storage: incompatible attempt save failed", err)
	}
	if err := state.tx.Commit(ctx); err != nil {
		return 0, operationError(ctx, "storage: incompatible attempt commit failed", err)
	}
	return configuration.Revision, nil
}

func ResumeAgentOwnership(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation int64, clock func() time.Time) (AgentOwnershipResult, error) {
	return ResumeAgentOwnershipWithHello(ctx, databaseURL, credentialID, credentialVerifier, resumeVerifier, generation, nil, clock)
}

func ResumeAgentOwnershipWithHello(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation int64, hello *AgentRuntimeHello, clock func() time.Time) (AgentOwnershipResult, error) {
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
	result, err := ensureAgentRuntimeGrant(ctx, state.queries, latest)
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent runtime state unavailable", err)
	}
	if hello != nil {
		if err := saveAgentRuntimeHello(ctx, state.queries, state.agentID, latest.Generation, *hello, now); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent hello status save failed", err)
		}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership resume commit failed", err)
	}
	return result, nil
}

func RenewAgentOwnership(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, expectedSequence int64, nonce [32]byte, clock func() time.Time) (AgentOwnershipResult, error) {
	return RenewAgentOwnershipWithHeartbeat(ctx, databaseURL, credentialID, credentialVerifier, resumeVerifier,
		generation, expectedSequence, nonce, nil, clock)
}

func RenewAgentOwnershipWithHeartbeat(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, expectedSequence int64, nonce [32]byte, heartbeat *AgentRuntimeHeartbeat, clock func() time.Time) (AgentOwnershipResult, error) {
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
		if heartbeat != nil {
			previous, previousErr := state.queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
				AgentID: state.agentID, Generation: generation, RenewalSequence: expectedSequence,
			})
			if previousErr != nil || previous.LeaseID != heartbeat.LeaseID {
				return AgentOwnershipResult{}, errors.Join(ErrAgentOwnershipDenied, previousErr)
			}
		}
		result, err := agentExecutionResult(ctx, state.queries, latest, latest.RenewalSequence)
		if err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent grant replay lookup failed", err)
		}
		result.Nonce = append([]byte(nil), nonce[:]...)
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
	if heartbeat != nil {
		current, currentErr := state.queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
			AgentID: state.agentID, Generation: generation, RenewalSequence: latest.RenewalSequence,
		})
		if currentErr != nil || current.LeaseID != heartbeat.LeaseID {
			return AgentOwnershipResult{}, errors.Join(ErrAgentOwnershipDenied, currentErr)
		}
	}
	if now.Before(latest.RenewedAt.Time.Add(agentMinimumRenewalInterval)) {
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
	updatedOwner := latest
	updatedOwner.RenewedAt = timestamp(now)
	updatedOwner.ExpiresAt = timestamp(newExpiry)
	updatedOwner.RenewalSequence = nextSequence
	updatedOwner.LastNonce = nonce[:]
	updatedOwner.LastRenewalIssuedAt = timestamp(now)
	updatedOwner.LastRenewalExpiresAt = timestamp(newExpiry)
	if err := prepareSliPartitions(ctx, state.tx, now); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: SLI partition preparation failed", err)
	}
	leaseID, err := newExecutionLeaseID()
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	if err := insertAgentExecutionGrant(ctx, state.queries, updatedOwner, leaseID, nonce[:]); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent execution grant renewal failed", err)
	}
	if err := state.queries.DeleteExpiredAgentExecutionGrants(ctx, dbgen.DeleteExpiredAgentExecutionGrantsParams{
		AgentID: state.agentID, RetainUntil: timestamp(now),
	}); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: expired agent grant pruning failed", err)
	}
	result, err := agentExecutionResult(ctx, state.queries, updatedOwner, nextSequence)
	if err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: renewed agent grant lookup failed", err)
	}
	if heartbeat != nil {
		if err := updateAgentRuntimeHeartbeat(ctx, state.queries, state.agentID, generation, *heartbeat); err != nil {
			return AgentOwnershipResult{}, operationError(ctx, "storage: agent heartbeat status update failed", err)
		}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentOwnershipResult{}, operationError(ctx, "storage: agent ownership renewal commit failed", err)
	}
	return result, nil
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

func ensureAgentRuntimeGrant(ctx context.Context, queries *dbgen.Queries, owner dbgen.AlmanacAgentOwnership) (AgentOwnershipResult, error) {
	grant, err := queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
		AgentID: owner.AgentID, Generation: owner.Generation, RenewalSequence: owner.RenewalSequence,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		leaseID, idErr := newExecutionLeaseID()
		if idErr != nil {
			return AgentOwnershipResult{}, idErr
		}
		nonce := owner.LastNonce
		if err := insertAgentExecutionGrant(ctx, queries, owner, leaseID, nonce); err != nil {
			return AgentOwnershipResult{}, err
		}
		grant, err = queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
			AgentID: owner.AgentID, Generation: owner.Generation, RenewalSequence: owner.RenewalSequence,
		})
	}
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	if !owner.CurrentLeaseID.Valid || owner.CurrentLeaseID.String != grant.LeaseID {
		changed, err := queries.SetAgentCurrentExecutionLease(ctx, dbgen.SetAgentCurrentExecutionLeaseParams{
			AgentID: owner.AgentID, Generation: owner.Generation, CurrentLeaseID: pgtype.Text{String: grant.LeaseID, Valid: true},
		})
		if err != nil || changed != 1 {
			return AgentOwnershipResult{}, errors.Join(err, ErrAgentOwnershipDenied)
		}
	}
	configuration, err := ensureAgentConfiguration(ctx, queries, owner.AgentID, owner.AcquiredAt.Time.UTC())
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	result := agentOwnershipResult(owner, grant.RequestNonce)
	result.LeaseID = grant.LeaseID
	result.ConfigurationRevision = grant.ConfigurationRevision
	result.ConfigurationSnapshot = append([]byte(nil), configuration.Snapshot...)
	result.AllowedMeasurements = append([]byte(nil), grant.AllowedMeasurements...)
	result.LeaseValidFrom = grant.ValidFrom.Time.UTC()
	result.LeaseValidUntil = grant.ValidUntil.Time.UTC()
	if owner.RenewalSequence > 0 {
		result.IssuedAt = grant.ValidFrom.Time.UTC()
		result.ExpiresAt = grant.ValidUntil.Time.UTC()
	}
	return result, nil
}

func agentExecutionResult(ctx context.Context, queries *dbgen.Queries, owner dbgen.AlmanacAgentOwnership, sequence int64) (AgentOwnershipResult, error) {
	grant, err := queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
		AgentID: owner.AgentID, Generation: owner.Generation, RenewalSequence: sequence,
	})
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	configuration, err := ensureAgentConfiguration(ctx, queries, owner.AgentID, owner.AcquiredAt.Time.UTC())
	if err != nil {
		return AgentOwnershipResult{}, err
	}
	result := agentOwnershipResult(owner, grant.RequestNonce)
	result.LeaseID = grant.LeaseID
	result.ConfigurationRevision = grant.ConfigurationRevision
	result.ConfigurationSnapshot = append([]byte(nil), configuration.Snapshot...)
	result.AllowedMeasurements = append([]byte(nil), grant.AllowedMeasurements...)
	result.LeaseValidFrom = grant.ValidFrom.Time.UTC()
	result.LeaseValidUntil = grant.ValidUntil.Time.UTC()
	result.IssuedAt = grant.ValidFrom.Time.UTC()
	result.ExpiresAt = grant.ValidUntil.Time.UTC()
	return result, nil
}

func insertAgentExecutionGrant(ctx context.Context, queries *dbgen.Queries, owner dbgen.AlmanacAgentOwnership, leaseID string, nonce []byte) error {
	configuration, err := ensureAgentConfiguration(ctx, queries, owner.AgentID, owner.AcquiredAt.Time.UTC())
	if err != nil {
		return err
	}
	if runtime, runtimeErr := queries.GetAgentRuntimeStatus(ctx, owner.AgentID); runtimeErr == nil && runtime.Protocol == agentprotocol.ProtocolV2 && runtime.AppliedConfigurationRevision > 0 && runtime.AppliedConfigurationRevision < configuration.Revision {
		transitionRevision, transitionErr := queries.GetAgentHandoffConfiguration(ctx, owner.AgentID)
		if transitionErr == nil && transitionRevision == configuration.Revision {
			configuration.Revision = runtime.AppliedConfigurationRevision
		}
		if transitionErr != nil && !errors.Is(transitionErr, pgx.ErrNoRows) {
			return transitionErr
		}
	} else if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
		return runtimeErr
	}
	allowed, err := authorizedMeasurementGrants(ctx, queries, owner, configuration.Revision)
	if err != nil {
		return err
	}
	validFrom := owner.AcquiredAt.Time.UTC()
	if owner.RenewalSequence > 0 && owner.LastRenewalIssuedAt.Valid {
		validFrom = owner.LastRenewalIssuedAt.Time.UTC()
	}
	var requestNonce []byte
	if len(nonce) > 0 {
		requestNonce = append([]byte(nil), nonce...)
	}
	if err := queries.InsertAgentExecutionGrant(ctx, dbgen.InsertAgentExecutionGrantParams{
		LeaseID: leaseID, AgentID: owner.AgentID, Generation: owner.Generation,
		ConfigurationRevision: configuration.Revision, AllowedMeasurements: allowed, ValidFrom: timestamp(validFrom),
		ValidUntil:      owner.ExpiresAt,
		RenewalSequence: owner.RenewalSequence, RequestNonce: requestNonce,
		RetainUntil: timestamp(owner.ExpiresAt.Time.Add(agentGrantRetention)),
	}); err != nil {
		return err
	}
	changed, err := queries.SetAgentCurrentExecutionLease(ctx, dbgen.SetAgentCurrentExecutionLeaseParams{
		AgentID: owner.AgentID, Generation: owner.Generation, CurrentLeaseID: pgtype.Text{String: leaseID, Valid: true},
	})
	if err != nil || changed != 1 {
		return errors.Join(err, ErrAgentOwnershipDenied)
	}
	return nil
}

func authorizedMeasurementGrants(ctx context.Context, queries *dbgen.Queries, owner dbgen.AlmanacAgentOwnership, configurationRevision int64) ([]byte, error) {
	grants := []agentprotocol.MeasurementGrant{}
	runtime, err := queries.GetAgentRuntimeStatus(ctx, owner.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return json.Marshal(grants)
	}
	if err != nil {
		return nil, err
	}
	if runtime.Generation != owner.Generation || runtime.AppliedConfigurationRevision < configurationRevision || runtime.ConfigurationFailureCode.Valid {
		return json.Marshal(grants)
	}
	rows, err := queries.ListAuthorizedMeasurementGrantsForAgent(ctx, dbgen.ListAuthorizedMeasurementGrantsForAgentParams{AgentID: owner.AgentID, ConfigurationRevision: configurationRevision})
	if err != nil {
		return nil, err
	}
	grants = make([]agentprotocol.MeasurementGrant, 0, len(rows))
	for _, row := range rows {
		if row.MeasurementRevision <= 0 {
			return nil, ErrAgentOwnershipDenied
		}
		grants = append(grants, agentprotocol.MeasurementGrant{
			SLIID: row.SliID.String(), MeasurementRevision: strconv.FormatInt(row.MeasurementRevision, 10),
		})
	}
	encoded, err := json.Marshal(grants)
	if err != nil {
		return nil, errors.New("storage: agent measurement grant encoding failed")
	}
	return encoded, nil
}

func ensureAgentConfiguration(ctx context.Context, queries *dbgen.Queries, agentID pgtype.UUID, createdAt time.Time) (dbgen.GetAgentConfigurationRow, error) {
	snapshot, err := json.Marshal(struct {
		AgentID               string                               `json:"agent_id"`
		ConfigurationRevision string                               `json:"configuration_revision"`
		Assignments           []agentprotocol.DatasourceAssignment `json:"assignments"`
		SLIs                  []agentprotocol.SLICollection        `json:"slis"`
	}{AgentID: agentID.String(), ConfigurationRevision: "1", Assignments: []agentprotocol.DatasourceAssignment{}, SLIs: []agentprotocol.SLICollection{}})
	if err != nil {
		return dbgen.GetAgentConfigurationRow{}, err
	}
	if err := queries.EnsureAgentConfiguration(ctx, dbgen.EnsureAgentConfigurationParams{
		AgentID: agentID, Snapshot: snapshot, CreatedAt: timestamp(createdAt),
	}); err != nil {
		return dbgen.GetAgentConfigurationRow{}, err
	}
	return queries.GetAgentConfiguration(ctx, agentID)
}

func saveAgentRuntimeHello(ctx context.Context, queries *dbgen.Queries, agentID pgtype.UUID, generation int64, hello AgentRuntimeHello, at time.Time) error {
	if hello.Protocol == "" || len(hello.Protocol) > 64 || hello.SoftwareVersion == "" || len(hello.SoftwareVersion) > 128 || len(hello.Capabilities) == 0 {
		return ErrAgentOwnershipInput
	}
	configuration, err := queries.GetAgentConfiguration(ctx, agentID)
	if err != nil || configuration.Revision <= 0 {
		return errors.Join(err, ErrAgentOwnershipInput)
	}
	if err := queries.UpsertAgentRuntimeHello(ctx, dbgen.UpsertAgentRuntimeHelloParams{
		AgentID: agentID, Generation: generation, Protocol: hello.Protocol,
		SoftwareVersion: hello.SoftwareVersion, Capabilities: hello.Capabilities,
		DesiredConfigurationRevision: configuration.Revision, UpdatedAt: timestamp(at),
	}); err != nil {
		return err
	}
	return queries.DeleteAgentIncompatibleAttempt(ctx, agentID)
}

func updateAgentRuntimeHeartbeat(ctx context.Context, queries *dbgen.Queries, agentID pgtype.UUID, generation int64, heartbeat AgentRuntimeHeartbeat) error {
	if heartbeat.ReceivedAt.IsZero() || heartbeat.LeaseID == "" || heartbeat.AppliedConfigurationRevision < 0 || heartbeat.SchedulerHealth == "" || heartbeat.CollectionProgress == "" {
		return ErrAgentOwnershipInput
	}
	updated, err := queries.UpdateAgentRuntimeHeartbeat(ctx, dbgen.UpdateAgentRuntimeHeartbeatParams{
		ReceivedAt:                   timestamp(heartbeat.ReceivedAt),
		AppliedConfigurationRevision: heartbeat.AppliedConfigurationRevision,
		SchedulerHealth:              heartbeat.SchedulerHealth,
		CollectionProgress:           heartbeat.CollectionProgress,
		LastAttemptAt:                optionalTimestamp(heartbeat.LastAttemptAt),
		LastSuccessfulQueryAt:        optionalTimestamp(heartbeat.LastSuccessfulQueryAt),
		NextExpectedExecutionAt:      optionalTimestamp(heartbeat.NextExpectedExecutionAt),
		AgentID:                      agentID, Generation: generation,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrAgentOwnershipDenied
	}
	if heartbeat.AppliedConfigurationRevision > 0 {
		configuration, configErr := queries.GetAgentConfiguration(ctx, agentID)
		if configErr != nil {
			return configErr
		}
		if configuration.Revision == heartbeat.AppliedConfigurationRevision {
			if err := reconcilePendingSliActivation(ctx, queries, agentID, heartbeat.AppliedConfigurationRevision, heartbeat.ReceivedAt.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

func optionalTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(value.UTC())
}

func RecordAgentConfigurationApplied(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, revision int64, clock func() time.Time) error {
	return RecordAgentConfigurationAppliedProtocol(ctx, databaseURL, credentialID, credentialVerifier, resumeVerifier, generation, revision, agentprotocol.ProtocolV1, clock)
}

func RecordAgentConfigurationAppliedProtocol(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, revision int64, protocol string, clock func() time.Time) error {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || generation <= 0 || revision <= 0 || clock == nil || (protocol != agentprotocol.ProtocolV1 && protocol != agentprotocol.ProtocolV2) {
		return ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return err
	}
	defer closeAgentOwnershipTransaction(state)
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAgentOwnershipDenied
	}
	if err != nil {
		return operationError(ctx, "storage: agent applied configuration owner lookup failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return ErrInvalidAgentCredential
	}
	if owner.Generation != generation || !now.Before(owner.ExpiresAt.Time) || subtle.ConstantTimeCompare(owner.ResumeVerifier, resumeVerifier[:]) != 1 {
		return ErrAgentOwnershipDenied
	}
	configuration, err := state.queries.GetAgentConfiguration(ctx, state.agentID)
	if err != nil || revision > configuration.Revision {
		return errors.Join(ErrAgentOwnershipDenied, err)
	}
	if revision != configuration.Revision {
		if protocol != agentprotocol.ProtocolV2 {
			return ErrAgentOwnershipDenied
		}
		known, knownErr := state.queries.AgentConfigurationRevisionExists(ctx, dbgen.AgentConfigurationRevisionExistsParams{AgentID: state.agentID, Revision: revision})
		if knownErr != nil || !known {
			return errors.Join(ErrAgentOwnershipDenied, knownErr)
		}
	}
	updated, err := state.queries.UpdateAgentAppliedConfiguration(ctx, dbgen.UpdateAgentAppliedConfigurationParams{
		AppliedConfigurationRevision: revision, ReceivedAt: timestamp(now), AgentID: state.agentID, Generation: generation,
	})
	if err != nil {
		return operationError(ctx, "storage: agent applied configuration update failed", err)
	}
	if updated != 1 {
		return ErrAgentOwnershipDenied
	}
	if protocol == agentprotocol.ProtocolV2 {
		if _, err := state.queries.DeleteAcknowledgedAgentHandoffConfiguration(ctx, dbgen.DeleteAcknowledgedAgentHandoffConfigurationParams{AgentID: state.agentID, Revision: revision}); err != nil {
			return operationError(ctx, "storage: handoff configuration acknowledgment failed", err)
		}
		if _, err := state.queries.MarkDatasourceReassignmentDrained(ctx, dbgen.MarkDatasourceReassignmentDrainedParams{SourceAgentID: state.agentID, RemovalRevision: revision, DrainedAt: timestamp(now)}); err != nil {
			return operationError(ctx, "storage: datasource drain acknowledgment failed", err)
		}
	}
	if revision == configuration.Revision {
		if err := reconcilePendingSliActivation(ctx, state.queries, state.agentID, revision, now); err != nil {
			return operationError(ctx, "storage: SLI activation reconciliation failed", err)
		}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return operationError(ctx, "storage: agent applied configuration commit failed", err)
	}
	return nil
}

func RecordAgentConfigurationFailed(ctx context.Context, databaseURL, credentialID string, credentialVerifier, resumeVerifier [32]byte, generation, revision int64, failureCode string, clock func() time.Time) error {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || generation <= 0 || revision <= 0 || clock == nil ||
		(failureCode != "invalid_snapshot" && failureCode != "incompatible" && failureCode != "apply_failed") {
		return ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return err
	}
	defer closeAgentOwnershipTransaction(state)
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAgentOwnershipDenied
	}
	if err != nil {
		return operationError(ctx, "storage: agent configuration failure owner lookup failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return ErrInvalidAgentCredential
	}
	if owner.Generation != generation || !now.Before(owner.ExpiresAt.Time) || subtle.ConstantTimeCompare(owner.ResumeVerifier, resumeVerifier[:]) != 1 {
		return ErrAgentOwnershipDenied
	}
	updated, err := state.queries.RecordAgentConfigurationFailed(ctx, dbgen.RecordAgentConfigurationFailedParams{
		ConfigurationFailureCode:    pgtype.Text{String: failureCode, Valid: true},
		FailedConfigurationRevision: pgtype.Int8{Int64: revision, Valid: true},
		ReceivedAt:                  timestamp(now), AgentID: state.agentID, Generation: generation,
	})
	if err != nil {
		return operationError(ctx, "storage: agent configuration failure update failed", err)
	}
	if updated != 1 {
		return ErrAgentOwnershipDenied
	}
	if err := state.tx.Commit(ctx); err != nil {
		return operationError(ctx, "storage: agent configuration failure commit failed", err)
	}
	return nil
}

type AgentConfigurationSnapshot struct {
	AgentID  string
	Revision int64
	Snapshot []byte
	Pending  bool
}

func ReadCurrentAgentConfiguration(ctx context.Context, databaseURL, credentialID string, credentialVerifier [32]byte, clock func() time.Time, adapters ...*sourcesecret.Adapter) (AgentConfigurationSnapshot, error) {
	credentialUUID, valid := canonicalUUID(credentialID)
	if !valid || clock == nil {
		return AgentConfigurationSnapshot{}, ErrAgentOwnershipInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credentialUUID, credentialVerifier)
	if err != nil {
		return AgentConfigurationSnapshot{}, err
	}
	defer closeAgentOwnershipTransaction(state)
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConfigurationSnapshot{}, ErrAgentOwnershipDenied
	}
	if err != nil {
		return AgentConfigurationSnapshot{}, operationError(ctx, "storage: agent configuration owner lookup failed", err)
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) || !now.Before(owner.ExpiresAt.Time) {
		return AgentConfigurationSnapshot{}, ErrInvalidAgentCredential
	}
	grant, err := state.queries.GetAgentExecutionGrantBySequence(ctx, dbgen.GetAgentExecutionGrantBySequenceParams{
		AgentID: state.agentID, Generation: owner.Generation, RenewalSequence: owner.RenewalSequence,
	})
	if err != nil || !owner.CurrentLeaseID.Valid || owner.CurrentLeaseID.String != grant.LeaseID {
		return AgentConfigurationSnapshot{}, errors.Join(ErrAgentOwnershipDenied, err)
	}
	configuration, err := ensureAgentConfiguration(ctx, state.queries, state.agentID, owner.AcquiredAt.Time.UTC())
	if err != nil {
		return AgentConfigurationSnapshot{}, operationError(ctx, "storage: agent configuration read failed", err)
	}
	if grant.ConfigurationRevision > configuration.Revision {
		return AgentConfigurationSnapshot{}, ErrAgentOwnershipDenied
	}
	if grant.ConfigurationRevision < configuration.Revision {
		runtime, runtimeErr := state.queries.GetAgentRuntimeStatus(ctx, state.agentID)
		transitionRevision, transitionErr := state.queries.GetAgentHandoffConfiguration(ctx, state.agentID)
		if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
			return AgentConfigurationSnapshot{}, runtimeErr
		}
		if transitionErr != nil && !errors.Is(transitionErr, pgx.ErrNoRows) {
			return AgentConfigurationSnapshot{}, transitionErr
		}
		if runtimeErr != nil || transitionErr != nil || runtime.Protocol != agentprotocol.ProtocolV2 || transitionRevision != configuration.Revision {
			if err := state.tx.Commit(ctx); err != nil {
				return AgentConfigurationSnapshot{}, operationError(ctx, "storage: pending agent configuration read commit failed", err)
			}
			return AgentConfigurationSnapshot{AgentID: state.agentID.String(), Revision: configuration.Revision, Pending: true}, nil
		}
	}
	var adapter *sourcesecret.Adapter
	if len(adapters) > 1 {
		return AgentConfigurationSnapshot{}, ErrAgentOwnershipInput
	}
	if len(adapters) == 1 {
		adapter = adapters[0]
	}
	installationID, err := loadInstallationID(ctx, state.tx)
	if err != nil {
		return AgentConfigurationSnapshot{}, operationError(ctx, "storage: agent datasource installation lookup failed", err)
	}
	snapshotBytes, err := buildDatasourceConfigurationSnapshot(ctx, state.queries, installationID, state.agentID, configuration.Revision, adapter)
	if err != nil {
		return AgentConfigurationSnapshot{}, err
	}
	if err := state.tx.Commit(ctx); err != nil {
		return AgentConfigurationSnapshot{}, operationError(ctx, "storage: agent configuration read commit failed", err)
	}
	return AgentConfigurationSnapshot{AgentID: state.agentID.String(), Revision: configuration.Revision, Snapshot: snapshotBytes}, nil
}

func buildDatasourceConfigurationSnapshot(ctx context.Context, queries *dbgen.Queries, installationID string, agentID pgtype.UUID, revision int64, adapter *sourcesecret.Adapter) ([]byte, error) {
	rows, err := queries.ListDatasources(ctx, dbgen.ListDatasourcesParams{
		AgentID: agentID, AfterCreatedAt: timestamp(time.Time{}), AfterID: pgtype.UUID{Valid: true}, RowLimit: 257,
	})
	if err != nil {
		return nil, operationError(ctx, "storage: agent datasource configuration read failed", err)
	}
	assignments := make([]agentprotocol.DatasourceAssignment, 0, len(rows))
	for _, row := range rows {
		transition, transitionErr := queries.GetDatasourceReassignment(ctx, row.ID)
		if transitionErr != nil && !errors.Is(transitionErr, pgx.ErrNoRows) {
			return nil, transitionErr
		}
		if transitionErr == nil && transition.SourceAgentID == agentID {
			if transition.UsesV2Removal {
				continue
			}
			row.Version = transition.SourceConfigVersion
		}
		assignment, assignmentErr := releaseDatasourceAssignment(installationID, row, adapter)
		if assignmentErr != nil {
			return nil, ErrAgentOwnershipDenied
		}
		assignments = append(assignments, assignment)
	}
	sliRows, err := queries.ListSliConfigurationForAgent(ctx, agentID)
	if err != nil {
		return nil, operationError(ctx, "storage: agent SLI configuration read failed", err)
	}
	slis := make([]agentprotocol.SLICollection, 0, len(sliRows))
	for _, row := range sliRows {
		slis = append(slis, agentprotocol.SLICollection{
			SLIID: row.SliID.String(), MeasurementRevision: strconv.FormatInt(row.MeasurementRevision, 10),
			DatasourceID: row.DatasourceID.String(), DatasourceVersion: strconv.FormatInt(row.DatasourceVersion, 10),
			MeasurementKind: agentprotocol.MeasurementKind(row.MeasurementKind), ComponentMode: agentprotocol.ComponentMode(row.ComponentMode),
			GoodQuery: row.GoodQuery, TotalQuery: row.TotalQuery, EffectiveFrom: row.EffectiveFrom.Time.UTC(),
		})
	}
	snapshotBytes, err := encodeAgentConfigurationSnapshot(uuidFromPG(agentID), revision, assignments, slis)
	if err != nil {
		return nil, err
	}
	if _, err := agentprotocol.DecodeConfigurationSnapshot(snapshotBytes); err != nil {
		return nil, ErrDatasourceConfigurationTooLarge
	}
	return snapshotBytes, nil
}

func encodeDatasourceConfigurationSnapshot(agentID string, revision int64, assignments []agentprotocol.DatasourceAssignment) ([]byte, error) {
	return encodeAgentConfigurationSnapshot(agentID, revision, assignments, []agentprotocol.SLICollection{})
}

func encodeAgentConfigurationSnapshot(agentID string, revision int64, assignments []agentprotocol.DatasourceAssignment, slis []agentprotocol.SLICollection) ([]byte, error) {
	if len(assignments) > 256 || len(slis) > agentprotocol.MaxSLICollections {
		return nil, ErrDatasourceConfigurationTooLarge
	}
	snapshotBytes, err := json.Marshal(agentprotocol.ConfigurationSnapshot{
		AgentID: agentID, ConfigurationRevision: strconv.FormatInt(revision, 10), Assignments: assignments, SLIs: slis,
	})
	if err != nil || len(snapshotBytes) > agentprotocol.MaxConfigurationBytes {
		return nil, ErrDatasourceConfigurationTooLarge
	}
	return snapshotBytes, nil
}

func releaseDatasourceAssignment(installationID string, row dbgen.AlmanacDatasource, adapter *sourcesecret.Adapter) (agentprotocol.DatasourceAssignment, error) {
	var tlsSettings datasource.TLSSettings
	if json.Unmarshal(row.Tls, &tlsSettings) != nil {
		return agentprotocol.DatasourceAssignment{}, ErrSourceSecretUnavailable
	}
	assignment := agentprotocol.DatasourceAssignment{
		DatasourceID: row.ID.String(), DatasourceVersion: strconv.FormatInt(row.Version, 10), Name: row.Name,
		Provider: row.Provider, ConfigurationFormat: row.ConfigurationFormat, Endpoint: row.Endpoint,
		TLS:                agentprotocol.DatasourceTLS{InsecureSkipVerify: tlsSettings.InsecureSkipVerify, CAPEM: tlsSettings.CAPEM},
		AuthenticationMode: row.AuthenticationMode, PollingIntervalSeconds: int(row.PollingIntervalSeconds),
		SettlingDelaySeconds: int(row.SettlingDelaySeconds),
	}
	if row.AuthenticationUsername.Valid {
		assignment.Username = row.AuthenticationUsername.String
	}
	if row.AuthenticationMode == "basic" {
		plaintext, err := decryptDatasourceSecret(installationID, row, "auth.password", row.PasswordRevision, row.PasswordEnvelope, adapter)
		if err != nil {
			return agentprotocol.DatasourceAssignment{}, err
		}
		assignment.Password = string(plaintext)
		clear(plaintext)
	}
	if row.AuthenticationMode == "bearer" {
		plaintext, err := decryptDatasourceSecret(installationID, row, "auth.token", row.TokenRevision, row.TokenEnvelope, adapter)
		if err != nil {
			return agentprotocol.DatasourceAssignment{}, err
		}
		assignment.BearerToken = string(plaintext)
		clear(plaintext)
	}
	if assignment.Validate() != nil {
		return agentprotocol.DatasourceAssignment{}, ErrSourceSecretUnavailable
	}
	return assignment, nil
}

func decryptDatasourceSecret(installationID string, row dbgen.AlmanacDatasource, field string, revision int64, raw []byte, adapter *sourcesecret.Adapter) ([]byte, error) {
	if adapter == nil || revision <= 0 || len(raw) == 0 {
		return nil, ErrSourceSecretUnavailable
	}
	var envelope sourcesecret.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, ErrSourceSecretUnavailable
	}
	plaintext, err := adapter.Decrypt(sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: row.ID.String(), Field: field, Revision: uint64(revision)}, envelope)
	if err != nil {
		return nil, ErrSourceSecretUnavailable
	}
	return plaintext, nil
}

func newExecutionLeaseID() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
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
