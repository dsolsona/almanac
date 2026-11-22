package storage

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func validateBackfillResult(result sli.BackfillChunkResult) error {
	if !canonicalUUIDCheck(result.ChunkID) || result.Generation < 1 || result.RetryAfterSeconds < 0 || result.RetryAfterSeconds > 86400 {
		return ErrObservationInvalid
	}
	if result.Failure != "" {
		failure := sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: result.Failure}
		if len(result.Outcomes) != 0 || failure.Validate() != nil || (result.RetryAfterSeconds > 0 && result.Failure != "rate_limited") {
			return ErrObservationInvalid
		}
		return nil
	}
	if len(result.Outcomes) == 0 || len(result.Outcomes) > 256 {
		return ErrObservationInvalid
	}
	rateLimited := false
	for _, outcome := range result.Outcomes {
		rateLimited = rateLimited || outcome.ErrorCode == "rate_limited"
		switch outcome.State {
		case "":
			if outcome.GoodCount != "" || outcome.TotalCount != "" || outcome.ErrorCode != "" || len(outcome.MissingComponents) != 0 {
				return ErrObservationInvalid
			}
		case sli.OutcomeObserved, sli.OutcomeCompleteEmpty, sli.OutcomeFailed:
			if outcome.Validate() != nil {
				return ErrObservationInvalid
			}
		default:
			return ErrObservationInvalid
		}
	}
	if result.RetryAfterSeconds > 0 && !rateLimited {
		return ErrObservationInvalid
	}
	return nil
}

// commitBackfillChunk fences progress and accepts new observations in one transaction.
func commitBackfillChunk(ctx context.Context, tx pgx.Tx, agentID string, sessionGeneration, configurationRevision int64, now time.Time, result sli.BackfillChunkResult) error {
	var sliID pgtype.UUID
	var revision int64
	var start, end time.Time
	var cadenceSeconds, retentionDays int
	err := tx.QueryRow(ctx, `
SELECT c.sli_id,c.measurement_revision,c.range_start,c.range_end,c.cadence_seconds,d.historical_retention_days
FROM public.almanac_backfill_chunks c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
WHERE c.id=$1 AND c.agent_id=$2 AND c.generation=$3 AND c.state='leased'
FOR UPDATE OF c`, result.ChunkID, agentID, result.Generation).Scan(&sliID, &revision, &start, &end, &cadenceSeconds, &retentionDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObservationDenied
	}
	if err != nil {
		return operationError(ctx, "storage: historical chunk read failed", err)
	}
	cadence := time.Duration(cadenceSeconds) * time.Second
	if !start.Add(time.Duration(len(result.Outcomes)) * cadence).Equal(end) {
		return ErrObservationInvalid
	}
	queries := dbgen.New(tx)
	measurement, err := queries.LockSliMeasurementForObservation(ctx, dbgen.LockSliMeasurementForObservationParams{
		SliID: sliID, Revision: revision, IntervalStart: timestamp(start),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObservationDenied
	}
	if err != nil {
		return operationError(ctx, "storage: historical measurement lock failed", err)
	}
	if measurement.CurrentAgentID.String() != agentID || start.Before(measurement.EffectiveFrom.Time) ||
		int(measurement.PollingIntervalSeconds) != cadenceSeconds {
		return ErrObservationDenied
	}

	covered, err := historicalCoverage(ctx, tx, sliID, revision, start, end)
	if err != nil {
		return operationError(ctx, "storage: historical coverage read failed", err)
	}
	cutoff := now.Add(-backfillRecoveryLookback(retentionDays))
	state, reason := "committed", ""
	conflict := false
	var starts []time.Time
	var ids []uuid.UUID
	var outcomes []string
	var good, total []pgtype.Text
	var digests [][]byte
	for index, outcome := range result.Outcomes {
		point := start.Add(time.Duration(index) * cadence)
		existing, exists := covered[point.UnixMicro()]
		if outcome.State == "" && !exists {
			return ErrObservationInvalid
		}
		if !exists && point.Before(cutoff) {
			if reason == "" {
				state, reason = "unavailable", "expired"
			}
			continue
		}
		if outcome.State == "" || (exists && outcome.State == sli.OutcomeFailed) {
			continue
		}
		if outcome.State == sli.OutcomeFailed {
			if reason == "" || reason == "expired" || outcome.ErrorCode == "authentication" || outcome.ErrorCode == "unsupported" {
				state, reason = "retry-wait", outcome.ErrorCode
				if reason == "authentication" || reason == "unsupported" || reason == "assignment_revoked" {
					state = "unavailable"
				}
			}
			continue
		}
		item := sli.ObservationItem{SLIID: sliID.String(), MeasurementRevision: strconv.FormatInt(revision, 10),
			IntervalStart: point, IntervalEnd: point.Add(cadence), Outcome: outcome}
		digest, err := canonicalSliResultDigest(item)
		if err != nil {
			return ErrObservationInvalid
		}
		if exists {
			conflict = conflict || !equalDigest(existing, digest[:])
			continue
		}
		executionID, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		starts = append(starts, point)
		ids = append(ids, executionID)
		outcomes = append(outcomes, string(outcome.State))
		good = append(good, nullableString(outcome.GoodCount))
		total = append(total, nullableString(outcome.TotalCount))
		digests = append(digests, digest[:])
	}

	if conflict && (state == "committed" || reason == "expired") {
		state, reason = "unavailable", "canonical_interval_conflict"
	}
	var accepted bool
	if err := tx.QueryRow(ctx, `SELECT public.almanac_finish_backfill_chunk($1,$2,$3,$4,$5,$6,$7)`,
		result.ChunkID, agentID, result.Generation, now, state, nullableString(reason), sessionGeneration).Scan(&accepted); err != nil {
		return operationError(ctx, "storage: fence historical chunk commit", err)
	}
	if !accepted {
		return ErrObservationDenied
	}
	if err := applyBackfillRetryAfter(ctx, tx, result, now); err != nil {
		return err
	}
	if conflict {
		if _, err := tx.Exec(ctx, `UPDATE public.almanac_backfill_chunks SET canonical_conflict=true WHERE id=$1`, result.ChunkID); err != nil {
			return operationError(ctx, "storage: record historical observation conflict", err)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	if err := ensureSliPartitions(ctx, tx, starts[0], starts[len(starts)-1]); err != nil {
		return operationError(ctx, "storage: historical observation partitions", err)
	}
	generation, err := queries.AdvanceSliSourceGeneration(ctx, dbgen.AdvanceSliSourceGenerationParams{
		SliID: sliID, MeasurementRevision: revision, RecordedAt: timestamp(now),
	})
	if err != nil {
		return operationError(ctx, "storage: historical observation generation failed", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.almanac_sli_observations
 (sli_id,measurement_revision,interval_start,outcome,good_count,total_count,result_digest,execution_id,delivery_id,
  agent_id,session_generation,configuration_revision,backfill_chunk_id,agent_created_at,recorded_at,ingestion_generation)
SELECT $1,$2,p.start,p.outcome,p.good::numeric,p.total::numeric,p.digest,p.id,p.id,$3,$4,$5,$6,$7,$7,$8
FROM unnest($9::timestamptz[],$10::uuid[],$11::text[],$12::text[],$13::text[],$14::bytea[])
  AS p(start,id,outcome,good,total,digest)`, sliID, revision, agentID, sessionGeneration,
		configurationRevision, result.ChunkID, now, generation, starts, ids, outcomes, good, total, digests); err != nil {
		return operationError(ctx, "storage: historical observation insert failed", err)
	}
	for index, point := range starts {
		hour := point.UTC().Truncate(time.Hour)
		if index+1 < len(starts) && starts[index+1].UTC().Truncate(time.Hour).Equal(hour) {
			continue
		}
		if err := queries.MarkSliObservation(ctx, dbgen.MarkSliObservationParams{
			SliID: sliID, MeasurementRevision: revision, Generation: generation,
			IntervalStart: timestamp(hour), IntervalEnd: timestamp(point.Add(cadence)), RecordedAt: timestamp(now),
		}); err != nil {
			return operationError(ctx, "storage: historical observation invalidation failed", err)
		}
	}
	return nil
}

func historicalCoverage(ctx context.Context, tx pgx.Tx, sliID pgtype.UUID, revision int64, start, end time.Time) (map[int64][]byte, error) {
	rows, err := tx.Query(ctx, `
SELECT interval_start,result_digest FROM public.almanac_sli_observations
WHERE sli_id=$1 AND measurement_revision=$2 AND interval_start >= $3 AND interval_start < $4
  AND outcome IN ('observed','complete_empty')`, sliID, revision, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	coverage := make(map[int64][]byte)
	for rows.Next() {
		var point time.Time
		var digest []byte
		if err := rows.Scan(&point, &digest); err != nil {
			return nil, err
		}
		coverage[point.UnixMicro()] = digest
	}
	return coverage, rows.Err()
}

func applyBackfillRetryAfter(ctx context.Context, tx pgx.Tx, result sli.BackfillChunkResult, now time.Time) error {
	if result.RetryAfterSeconds == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
WITH delayed AS (
    UPDATE public.almanac_backfill_chunks c
    SET next_attempt_at=LEAST(j.created_at+interval '24 hours',GREATEST(c.next_attempt_at,$2::timestamptz))
    FROM public.almanac_backfill_jobs j
    WHERE c.id=$1 AND c.job_id=j.id AND c.state='retry-wait' AND c.reason='rate_limited'
)
INSERT INTO public.almanac_backfill_source_cooldowns (datasource_id,source_version,retry_at)
SELECT m.datasource_id,c.source_version,$2
FROM public.almanac_backfill_chunks c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
WHERE c.id=$1
ON CONFLICT(datasource_id) DO UPDATE
SET retry_at=CASE WHEN almanac_backfill_source_cooldowns.source_version=excluded.source_version
                  THEN greatest(almanac_backfill_source_cooldowns.retry_at,excluded.retry_at)
                  ELSE excluded.retry_at END,
    source_version=excluded.source_version
WHERE almanac_backfill_source_cooldowns.source_version<=excluded.source_version`,
		result.ChunkID, now.Add(time.Duration(result.RetryAfterSeconds)*time.Second))
	if err != nil {
		return operationError(ctx, "storage: record historical retry guidance", err)
	}
	return nil
}
