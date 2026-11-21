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

// commitBackfillChunk must run within the caller's authorized historical transaction.
// A rejected or incomplete batch must roll back both progress and observation writes.
func commitBackfillChunk(ctx context.Context, tx pgx.Tx, agentID string, sessionGeneration, configurationRevision int64, now time.Time, result sli.BackfillChunkResult) error {
	var sliID pgtype.UUID
	var revision int64
	var start, end time.Time
	var cadenceSeconds int
	err := tx.QueryRow(ctx, `
SELECT sli_id, measurement_revision, range_start, range_end, cadence_seconds
FROM public.almanac_backfill_chunks WHERE id=$1 AND agent_id=$2 AND generation=$3 AND state='leased'`,
		result.ChunkID, agentID, result.Generation).Scan(&sliID, &revision, &start, &end, &cadenceSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObservationDenied
	}
	if err != nil {
		return operationError(ctx, "storage: historical chunk read failed", err)
	}
	cadence := time.Duration(cadenceSeconds) * time.Second
	if len(result.Outcomes) == 0 || len(result.Outcomes) > 256 || !start.Add(time.Duration(len(result.Outcomes))*cadence).Equal(end) {
		return ErrObservationInvalid
	}
	for _, outcome := range result.Outcomes {
		if (outcome.State != sli.OutcomeObserved && outcome.State != sli.OutcomeCompleteEmpty) || outcome.Validate() != nil {
			return ErrObservationInvalid
		}
	}

	if err := ensureSliPartitions(ctx, tx, start, end.Add(-cadence)); err != nil {
		return operationError(ctx, "storage: historical observation partitions", err)
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
	if measurement.CurrentAgentID.String() != agentID || start.Before(measurement.EffectiveFrom.Time) || start.Before(now.Add(-sli.ObservationRetention)) ||
		int(measurement.PollingIntervalSeconds) != cadenceSeconds {
		return ErrObservationDenied
	}

	var accepted bool
	if err := tx.QueryRow(ctx, `SELECT public.almanac_finish_backfill_chunk($1,$2,$3,$4,'committed',NULL,$5)`,
		result.ChunkID, agentID, result.Generation, now, sessionGeneration).Scan(&accepted); err != nil {
		return operationError(ctx, "storage: fence historical chunk commit", err)
	}
	if !accepted {
		return ErrObservationDenied
	}
	for index, outcome := range result.Outcomes {
		point := start.Add(time.Duration(index) * cadence)
		item := sli.ObservationItem{
			SLIID: sliID.String(), MeasurementRevision: strconv.FormatInt(revision, 10),
			IntervalStart: point, IntervalEnd: point.Add(cadence), Outcome: outcome,
		}
		if err := acceptBackfillObservation(ctx, tx, queries, sliID, revision, agentID, sessionGeneration, configurationRevision, now, result.ChunkID, item); err != nil {
			return err
		}
	}
	return nil
}

func acceptBackfillObservation(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, sliID pgtype.UUID, revision int64, agentID string, sessionGeneration, configurationRevision int64, now time.Time, chunkID string, item sli.ObservationItem) error {
	_, err := queries.GetSliCoverageByCanonicalInterval(ctx, dbgen.GetSliCoverageByCanonicalIntervalParams{
		SliID: sliID, MeasurementRevision: revision, IntervalStart: timestamp(item.IntervalStart),
	})
	if err == nil {
		// A live or earlier historical result already owns this canonical interval.
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return operationError(ctx, "storage: historical observation coverage read failed", err)
	}
	digest, err := canonicalSliResultDigest(item)
	if err != nil {
		return ErrObservationInvalid
	}
	good, total := nullableString(item.Outcome.GoodCount), nullableString(item.Outcome.TotalCount)
	generation, err := queries.AdvanceSliSourceGeneration(ctx, dbgen.AdvanceSliSourceGenerationParams{
		SliID: sliID, MeasurementRevision: revision, RecordedAt: timestamp(now),
	})
	if err != nil {
		return operationError(ctx, "storage: historical observation generation failed", err)
	}
	executionID, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.almanac_sli_observations
 (sli_id,measurement_revision,interval_start,outcome,good_count,total_count,result_digest,execution_id,delivery_id,
  agent_id,session_generation,configuration_revision,backfill_chunk_id,agent_created_at,recorded_at,ingestion_generation)
VALUES ($1,$2,$3,$4,$5::text::numeric,$6::text::numeric,$7,$8,$8,$9,$10,$11,$12,$13,$13,$14)`,
		sliID, revision, item.IntervalStart, item.Outcome.State, good, total, digest[:], executionID,
		agentID, sessionGeneration, configurationRevision, chunkID, now, generation); err != nil {
		return operationError(ctx, "storage: historical observation insert failed", err)
	}
	return queries.MarkSliObservation(ctx, dbgen.MarkSliObservationParams{
		SliID: sliID, MeasurementRevision: revision, Generation: generation,
		IntervalStart: timestamp(item.IntervalStart), IntervalEnd: timestamp(item.IntervalEnd), RecordedAt: timestamp(now),
	})
}
