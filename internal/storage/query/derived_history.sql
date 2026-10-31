-- name: FindPreparedSloHistory :one
SELECT b.payload, g.id AS generation_id, g.published_at
FROM public.almanac_slo_history_blocks b
JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.slo_id=$1 AND g.definition_revision=$2 AND g.measurement_revision=$3
  AND g.calculation_version=sqlc.arg(calculation_version)
  AND g.retention_start=sqlc.arg(retention_start)
  AND g.published_at>=sqlc.arg(now)::timestamptz-interval '5 minutes'
  AND g.source_generation=$4 AND g.evaluated_through=$5
  AND b.range_start=$6 AND b.range_end=$7 AND b.burn_window_seconds=$8
  AND b.kind=sqlc.arg(kind)
  AND b.payload_digest=sha256(convert_to(b.payload::text,'UTF8'))
ORDER BY g.published_at DESC LIMIT 1;

-- name: DeleteCorruptPreparedSloHistory :execrows
DELETE FROM public.almanac_slo_history_blocks WHERE generation_id=$1;

-- Exact table pages read only one bounded, immutable prepared generation.
-- name: ReadPreparedSloValuePage :one
SELECT g.id,g.source_generation,g.definition_revision,g.measurement_revision,
       g.calculation_version,g.evaluated_through,g.published_at,g.retention_start,
       jsonb_array_length(b.payload #> sqlc.arg(series_path)::text[])::integer AS total_values,
       coalesce((
           SELECT jsonb_agg(e.value ORDER BY e.ordinality)
           FROM jsonb_array_elements(b.payload #> sqlc.arg(series_path)::text[]) WITH ORDINALITY e
           WHERE e.ordinality>sqlc.arg(page_offset)::integer
             AND e.ordinality<=sqlc.arg(page_offset)::integer+sqlc.arg(page_limit)::integer
       ),'[]'::jsonb)::jsonb AS page_values
FROM public.almanac_slo_history_blocks b
JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.slo_id=sqlc.arg(slo_id) AND g.id=sqlc.arg(generation_id) AND b.kind='detail'
  AND g.published_at>=sqlc.arg(now)::timestamptz-interval '5 minutes'
  AND b.payload_digest=sha256(convert_to(b.payload::text,'UTF8'))
  AND g.calculation_version=sqlc.arg(calculation_version);

-- name: AdmitSloHistoryJob :execrows
INSERT INTO public.almanac_slo_history_jobs
    (slo_id,definition_revision,measurement_revision,source_generation,evaluated_through,
     range_start,range_end,burn_window_seconds,state,next_attempt_at,created_at,kind,calculation_version,retention_start)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$9,sqlc.arg(kind),sqlc.arg(calculation_version),sqlc.arg(retention_start)
WHERE (SELECT count(*) FROM public.almanac_slo_history_jobs) < 64
  AND (SELECT count(*) FROM public.almanac_slo_history_jobs WHERE slo_id=$1) < 8
  AND NOT EXISTS (
      SELECT 1 FROM public.almanac_slo_history_jobs failed
      WHERE failed.slo_id=$1 AND failed.definition_revision=$2 AND failed.measurement_revision=$3
        AND failed.state='failed' AND failed.burn_window_seconds=$8
        AND failed.kind=sqlc.arg(kind)
        AND failed.calculation_version=sqlc.arg(calculation_version)
        AND failed.range_end-failed.range_start=$7::timestamptz-$6::timestamptz
  )
ON CONFLICT (slo_id,definition_revision,measurement_revision,source_generation,evaluated_through,range_start,range_end,burn_window_seconds,kind,calculation_version,retention_start)
DO NOTHING;

-- name: ReadSloHistoryPreparationState :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_slo_history_jobs failed
    WHERE failed.slo_id=sqlc.arg(slo_id) AND failed.definition_revision=sqlc.arg(definition_revision)
      AND failed.measurement_revision=sqlc.arg(measurement_revision) AND failed.state='failed'
      AND failed.kind=sqlc.arg(kind)
      AND failed.calculation_version=sqlc.arg(calculation_version)
      AND failed.burn_window_seconds=sqlc.arg(burn_window_seconds)
      AND failed.range_end-failed.range_start=sqlc.arg(range_end)::timestamptz-sqlc.arg(range_start)::timestamptz
) AS exhausted, EXISTS (
    SELECT 1 FROM public.almanac_slo_history_jobs queued
    WHERE queued.slo_id=sqlc.arg(slo_id) AND queued.definition_revision=sqlc.arg(definition_revision)
      AND queued.measurement_revision=sqlc.arg(measurement_revision) AND queued.source_generation=sqlc.arg(source_generation)
      AND queued.evaluated_through=sqlc.arg(evaluated_through) AND queued.range_start=sqlc.arg(range_start)
      AND queued.range_end=sqlc.arg(range_end) AND queued.burn_window_seconds=sqlc.arg(burn_window_seconds)
      AND queued.kind=sqlc.arg(kind)
      AND queued.calculation_version=sqlc.arg(calculation_version) AND queued.retention_start=sqlc.arg(retention_start)
) AS queued;

-- A crash on the last permitted attempt must become an observable failure,
-- rather than remaining running forever with an unclaimable expired lease.
-- name: ExhaustExpiredSloHistoryJobs :execrows
UPDATE public.almanac_slo_history_jobs
SET state='failed', lease_until=NULL, last_error='history_lease_expired'
WHERE state='running' AND attempts>=5 AND lease_until <= $1;

-- name: RetryFailedSloHistoryJob :execrows
UPDATE public.almanac_slo_history_jobs target
SET state='pending',attempts=0,lease_until=NULL,lease_epoch=lease_epoch+1,
    next_attempt_at=$3,manual_retried_at=$3,last_error=NULL
WHERE target.id=$1 AND target.slo_id=$2 AND target.state='failed'
  AND (target.manual_retried_at IS NULL OR target.manual_retried_at<=$3::timestamptz-interval '1 minute')
  AND (SELECT count(*) FROM public.almanac_slo_history_jobs WHERE state<>'failed')<64
  AND (SELECT count(*) FROM public.almanac_slo_history_jobs WHERE slo_id=$2 AND state<>'failed')<8;

-- name: ClaimSloHistoryJob :one
WITH candidate AS (
    SELECT id FROM public.almanac_slo_history_jobs
    WHERE state <> 'failed' AND attempts < 5 AND next_attempt_at <= sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until <= sqlc.arg(now)::timestamptz)
    ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_slo_history_jobs j
SET state='running', attempts=j.attempts+1, lease_epoch=j.lease_epoch+1,
    lease_until=sqlc.arg(lease_until)::timestamptz
FROM candidate c WHERE j.id=c.id RETURNING j.*;

-- name: InsertPreparedSloHistory :exec
INSERT INTO public.almanac_slo_history_blocks
    (generation_id,range_start,range_end,burn_window_seconds,payload,kind,budget_sparkline,payload_digest)
VALUES ($1,$2,$3,$4,$5,$6,$7,sha256(convert_to($5::jsonb::text,'UTF8')));

-- name: LatestPreparedBudgetSparkline :one
SELECT (b.payload->'budget_series'->'samples')::jsonb AS budget_sparkline,g.id,g.source_generation,g.definition_revision,g.measurement_revision,
       g.exclusion_revision,g.calculation_version,g.evaluated_through,g.published_at
FROM public.almanac_slo_history_blocks b
JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.slo_id=$1 AND g.definition_revision=$2 AND g.measurement_revision=$3
  AND g.calculation_version=sqlc.arg(calculation_version)
  AND b.burn_window_seconds=3600 AND b.range_end-b.range_start=sqlc.arg(span_seconds)::bigint*interval '1 second'
  AND b.kind='overview'
  AND g.retention_start=sqlc.arg(retention_start)
  AND g.evaluated_through <= sqlc.arg(at)::timestamptz
  AND b.payload_digest=sha256(convert_to(b.payload::text,'UTF8'))
ORDER BY g.evaluated_through DESC,g.source_generation DESC,g.published_at DESC LIMIT 1;

-- name: AttachPreparedBudgetSparkline :execrows
WITH replacement AS (
    SELECT p.slo_id,jsonb_set(jsonb_set(jsonb_set(p.payload,'{budget_sparkline}',sqlc.arg(samples)::jsonb),
        '{budget_history_generation}',sqlc.arg(history_generation)::jsonb),'{budget_history_state}',
        to_jsonb(CASE WHEN g.source_generation=sqlc.arg(source_generation)::bigint AND g.evaluated_through=sqlc.arg(at)::timestamptz AND g.retention_start=sqlc.arg(retention_start) THEN 'ready'::text ELSE 'preparing'::text END)) AS payload
    FROM public.almanac_slo_current_projections p JOIN public.almanac_slo_derived_generations g ON p.generation_id=g.id
    WHERE p.slo_id=sqlc.arg(slo_id) AND g.definition_revision=sqlc.arg(definition_revision)
      AND g.measurement_revision=sqlc.arg(measurement_revision)
      AND p.payload_digest=sha256(convert_to(p.payload::text,'UTF8'))
)
UPDATE public.almanac_slo_current_projections p
SET payload=r.payload,payload_digest=sha256(convert_to(r.payload::text,'UTF8'))
FROM replacement r WHERE p.slo_id=r.slo_id;

-- name: CompleteSloHistoryJob :execrows
DELETE FROM public.almanac_slo_history_jobs WHERE id=$1 AND lease_epoch=$2 AND state='running';

-- name: FailSloHistoryJob :execrows
UPDATE public.almanac_slo_history_jobs
SET state=CASE WHEN attempts>=5 THEN 'failed' ELSE 'pending' END,
    lease_until=NULL,next_attempt_at=$3,last_error=$4
WHERE id=$1 AND lease_epoch=$2;

-- name: LockHistoryAdmission :exec
SELECT pg_advisory_xact_lock(1095520259, 1);

-- name: DeleteExpiredDerivedGenerations :execrows
DELETE FROM public.almanac_slo_derived_generations g
WHERE g.id IN (
    SELECT old.id FROM public.almanac_slo_derived_generations old
    WHERE old.published_at < $1
      AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_current_projections p WHERE p.generation_id=old.id)
    ORDER BY old.published_at LIMIT 100
);
