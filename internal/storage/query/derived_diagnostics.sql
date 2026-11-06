-- name: ListSloPreparationJobs :many
WITH objective AS (
    SELECT s.id,d.revision,d.sli_id,d.measurement_revision
    FROM public.almanac_slos s JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    WHERE s.id=sqlc.arg(slo_id)
), jobs AS (
    SELECT f.slo_id AS id,'current'::text AS stage,
           CASE WHEN f.attempts>=5 THEN 'failed'::text ELSE 'pending'::text END AS state,
           f.attempts,f.next_attempt_at,f.last_error
    FROM public.almanac_slo_current_failures f JOIN objective o ON f.slo_id=o.id
      AND f.definition_revision=o.revision AND f.measurement_revision=o.measurement_revision
    WHERE f.calculation_version=sqlc.arg(calculation_version)
    UNION ALL
    SELECT j.id,'history',j.state,j.attempts,j.next_attempt_at,coalesce(j.last_error,'')
    FROM public.almanac_slo_history_jobs j JOIN objective o ON j.slo_id=o.id
      AND j.definition_revision=o.revision AND j.measurement_revision=o.measurement_revision
    WHERE j.calculation_version=sqlc.arg(calculation_version)
    UNION ALL
    SELECT d.id,'aggregate','failed',d.attempts,d.next_attempt_at,coalesce(d.last_error,'aggregate_lease_expired')
    FROM public.almanac_derived_dirty_ranges d JOIN objective o ON d.sli_id=o.sli_id AND d.measurement_revision=o.measurement_revision
    WHERE d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
    UNION ALL
    SELECT w.id,'history',CASE WHEN w.attempts>=5 THEN 'failed' WHEN w.lease_until>sqlc.arg(now)::timestamptz THEN 'running' ELSE 'pending' END,
           w.attempts,w.next_attempt_at,coalesce(w.last_error,'')
    FROM public.almanac_slo_series_tile_work w JOIN objective o ON w.slo_id=o.id
      AND w.definition_revision=o.revision AND w.measurement_revision=o.measurement_revision
    WHERE w.calculation_version=sqlc.arg(calculation_version) AND w.exclusion_revision=0
    UNION ALL
    SELECT w.id,'history',CASE WHEN w.attempts>=5 THEN 'failed' WHEN w.lease_until>sqlc.arg(now)::timestamptz THEN 'running' ELSE 'pending' END,
           w.attempts,w.next_attempt_at,coalesce(w.last_error,'')
    FROM public.almanac_slo_series_repair w JOIN objective o ON w.slo_id=o.id
      AND w.definition_revision=o.revision AND w.measurement_revision=o.measurement_revision
    WHERE w.calculation_version=sqlc.arg(calculation_version) AND w.cursor_at<w.end_at
)
SELECT id,stage,state,attempts,next_attempt_at,last_error FROM jobs
ORDER BY (state='failed') DESC,next_attempt_at,id LIMIT 101;

-- name: RetryCurrentSloPreparation :execrows
UPDATE public.almanac_slo_current_failures f
SET attempts=0,next_attempt_at=sqlc.arg(now)::timestamptz,manual_retried_at=sqlc.arg(now)::timestamptz,last_error='',failing_since=NULL
WHERE f.slo_id=sqlc.arg(slo_id) AND f.definition_revision=sqlc.arg(definition_revision)
  AND f.measurement_revision=sqlc.arg(measurement_revision) AND f.calculation_version=sqlc.arg(calculation_version)
  AND f.attempts>=5 AND (f.manual_retried_at IS NULL OR f.manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');

-- name: RetrySourceAggregate :execrows
UPDATE public.almanac_derived_dirty_ranges d
SET attempts=0,lease_until=NULL,lease_epoch=d.lease_epoch+1,next_attempt_at=sqlc.arg(now)::timestamptz,
    manual_retried_at=sqlc.arg(now)::timestamptz,last_error=NULL,failing_since=NULL
WHERE d.id=sqlc.arg(job_id) AND d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
  AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
  AND d.last_error IS DISTINCT FROM 'retention_expired'
  AND (d.manual_retried_at IS NULL OR d.manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');

-- name: RetryRetainedTile :execrows
UPDATE public.almanac_slo_series_tile_work
SET attempts=0,lease_until=NULL,lease_epoch=lease_epoch+1,next_attempt_at=sqlc.arg(now)::timestamptz,
    manual_retried_at=sqlc.arg(now)::timestamptz,last_error=NULL,failing_since=NULL
WHERE id=sqlc.arg(job_id) AND slo_id=sqlc.arg(slo_id)
  AND definition_revision=sqlc.arg(definition_revision) AND measurement_revision=sqlc.arg(measurement_revision)
  AND calculation_version=sqlc.arg(calculation_version) AND exclusion_revision=0 AND attempts>=5
  AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
  AND (manual_retried_at IS NULL OR manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');

-- name: RetryRetainedRepair :execrows
UPDATE public.almanac_slo_series_repair
SET attempts=0,lease_until=NULL,lease_epoch=lease_epoch+1,next_attempt_at=sqlc.arg(now)::timestamptz,
    manual_retried_at=sqlc.arg(now)::timestamptz,last_error=NULL,failing_since=NULL
WHERE id=sqlc.arg(job_id) AND slo_id=sqlc.arg(slo_id)
  AND definition_revision=sqlc.arg(definition_revision) AND measurement_revision=sqlc.arg(measurement_revision)
  AND calculation_version=sqlc.arg(calculation_version) AND cursor_at<end_at AND attempts>=5
  AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
  AND (manual_retried_at IS NULL OR manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');
