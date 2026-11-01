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
)
SELECT id,stage,state,attempts,next_attempt_at,last_error FROM jobs
ORDER BY (state='failed') DESC,next_attempt_at,id LIMIT 101;

-- name: RetryCurrentSloPreparation :execrows
UPDATE public.almanac_slo_current_failures f
SET attempts=0,next_attempt_at=sqlc.arg(now)::timestamptz,manual_retried_at=sqlc.arg(now)::timestamptz,last_error=''
WHERE f.slo_id=sqlc.arg(slo_id) AND f.definition_revision=sqlc.arg(definition_revision)
  AND f.measurement_revision=sqlc.arg(measurement_revision) AND f.calculation_version=sqlc.arg(calculation_version)
  AND f.attempts>=5 AND (f.manual_retried_at IS NULL OR f.manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');

-- name: RetrySourceAggregate :execrows
UPDATE public.almanac_derived_dirty_ranges d
SET attempts=0,lease_until=NULL,lease_epoch=d.lease_epoch+1,next_attempt_at=sqlc.arg(now)::timestamptz,
    manual_retried_at=sqlc.arg(now)::timestamptz,last_error=NULL
WHERE d.id=sqlc.arg(job_id) AND d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
  AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
  AND (d.manual_retried_at IS NULL OR d.manual_retried_at<=sqlc.arg(now)::timestamptz-interval '1 minute');
