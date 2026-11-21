package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/jackc/pgx/v5"
)

type BackfillAgentProof struct {
	CredentialID string
	Verifier     [32]byte
	Generation   int64
}

func ClaimBackfill(ctx context.Context, databaseURL string, proof BackfillAgentProof, clock func() time.Time, runtimes ...*Runtime) (sli.BackfillClaim, error) {
	state, now, _, err := beginBackfillAgent(ctx, databaseURL, proof, clock, runtimes...)
	if err != nil {
		return sli.BackfillClaim{}, err
	}
	defer closeAgentOwnershipTransaction(state)
	var claim sli.BackfillClaim
	var retentionDays int
	err = state.tx.QueryRow(ctx, `
SELECT c.id, c.sli_id, m.datasource_id, c.generation, c.measurement_revision, c.range_start, c.range_end,
       c.cadence_seconds, c.lease_until,d.historical_retention_days,
       ARRAY(SELECT o.interval_start FROM public.almanac_sli_observations o
         WHERE o.sli_id=c.sli_id AND o.measurement_revision=c.measurement_revision
           AND o.interval_start>=c.range_start AND o.interval_start<c.range_end
           AND o.outcome IN ('observed','complete_empty') ORDER BY o.interval_start)
FROM public.almanac_claim_backfill_chunk($1,$2,$3) c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id`, state.agentID, now, proof.Generation).
		Scan(&claim.ID, &claim.SLIID, &claim.DatasourceID, &claim.Generation, &claim.Revision, &claim.Start, &claim.End, &claim.Cadence, &claim.LeaseUntil, &retentionDays, &claim.CoveredStarts)
	if errors.Is(err, pgx.ErrNoRows) {
		return sli.BackfillClaim{}, nil
	}
	if err != nil {
		return sli.BackfillClaim{}, operationError(ctx, "storage: claim historical chunk", err)
	}
	if claim.Start.Before(now.Add(-backfillRecoveryLookback(retentionDays))) {
		if _, err := state.tx.Exec(ctx, `SELECT public.almanac_finish_backfill_chunk($1,$2,$3,$4,'unavailable','expired',$5)`,
			claim.ID, state.agentID, claim.Generation, now, proof.Generation); err != nil {
			return sli.BackfillClaim{}, operationError(ctx, "storage: expire historical claim", err)
		}
		claim = sli.BackfillClaim{}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return sli.BackfillClaim{}, operationError(ctx, "storage: commit historical claim", err)
	}
	return claim, nil
}

func SubmitBackfill(ctx context.Context, databaseURL string, proof BackfillAgentProof, clock func() time.Time, result sli.BackfillChunkResult, runtimes ...*Runtime) error {
	state, now, revision, err := beginBackfillAgent(ctx, databaseURL, proof, clock, runtimes...)
	if err != nil {
		return err
	}
	defer closeAgentOwnershipTransaction(state)
	if !canonicalUUIDCheck(result.ChunkID) || result.Generation < 1 {
		return ErrObservationInvalid
	}
	if result.Failure == "" {
		if err := commitBackfillChunk(ctx, state.tx, state.agentID.String(), proof.Generation, revision, now, result); err != nil {
			return err
		}
	} else {
		outcome := sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: result.Failure}
		if len(result.Outcomes) != 0 || outcome.Validate() != nil {
			return ErrObservationInvalid
		}
		next := "retry-wait"
		switch result.Failure {
		case "authentication", "unsupported", "assignment_revoked":
			next = "unavailable"
		}
		var accepted bool
		err := state.tx.QueryRow(ctx, `SELECT public.almanac_finish_backfill_chunk($1,$2,$3,$4,$5,$6,$7)`,
			result.ChunkID, state.agentID, result.Generation, now, next, result.Failure, proof.Generation).Scan(&accepted)
		if err != nil {
			return operationError(ctx, "storage: record historical execution failure", err)
		}
		if !accepted {
			return ErrObservationDenied
		}
	}
	if err := state.tx.Commit(ctx); err != nil {
		return operationError(ctx, "storage: commit historical observations", err)
	}
	return nil
}

func beginBackfillAgent(ctx context.Context, databaseURL string, proof BackfillAgentProof, clock func() time.Time, runtimes ...*Runtime) (*agentOwnershipTransaction, time.Time, int64, error) {
	credentialID, valid := canonicalUUID(proof.CredentialID)
	if !valid || proof.Generation <= 0 || clock == nil {
		return nil, time.Time{}, 0, ErrInvalidAgentCredential
	}
	state, err := beginAgentOwnershipTraffic(ctx, databaseURL, credentialID, proof.Verifier, HistoricalTraffic, runtimes...)
	if err != nil {
		return nil, time.Time{}, 0, err
	}
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, proof.Verifier, now) {
		closeAgentOwnershipTransaction(state)
		return nil, time.Time{}, 0, ErrInvalidAgentCredential
	}
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (owner.Generation != proof.Generation || !now.Before(owner.ExpiresAt.Time))) {
		closeAgentOwnershipTransaction(state)
		return nil, time.Time{}, 0, ErrObservationDenied
	}
	if err != nil {
		closeAgentOwnershipTransaction(state)
		return nil, time.Time{}, 0, operationError(ctx, "storage: historical agent ownership lookup", err)
	}
	var revision int64
	err = state.tx.QueryRow(ctx, `
SELECT applied_configuration_revision
FROM public.almanac_agent_runtime_status
WHERE agent_id=$1 AND generation=$2 AND applied_configuration_revision=desired_configuration_revision
  AND last_heartbeat_at > $3::timestamptz - interval '1 minute'
  AND scheduler_health='ready'`, state.agentID, proof.Generation, now).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		closeAgentOwnershipTransaction(state)
		return nil, time.Time{}, 0, ErrObservationDenied
	}
	if err != nil {
		closeAgentOwnershipTransaction(state)
		return nil, time.Time{}, 0, operationError(ctx, "storage: historical configuration authority lookup", err)
	}
	return state, now, revision, nil
}
