-- Preview counts stop at 1001; the report labels these as lower bounds.
-- name: PreviewCalculatedRetention :one
SELECT
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_slo_extrema WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz LIMIT 1001) e)::bigint AS extrema,
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_sli_hourly_aggregates WHERE hour_start>=sqlc.arg(range_start)::timestamptz AND hour_start<sqlc.arg(range_end)::timestamptz LIMIT 1001) h)::bigint AS source_hours;

-- name: DeleteExpiredRetentionExtrema :execrows
WITH expired AS (
 SELECT slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start
 FROM public.almanac_slo_extrema
 WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz
 ORDER BY range_end LIMIT 1000 FOR UPDATE SKIP LOCKED
)
DELETE FROM public.almanac_slo_extrema e USING expired old
WHERE (e.slo_id,e.definition_revision,e.measurement_revision,e.exclusion_revision,e.calculation_version,e.resolution_seconds,e.range_start)
    =(old.slo_id,old.definition_revision,old.measurement_revision,old.exclusion_revision,old.calculation_version,old.resolution_seconds,old.range_start);

-- name: ExpireRetentionSourceWork :execrows
WITH expired AS (
 SELECT id FROM public.almanac_derived_dirty_ranges
 WHERE range_start<sqlc.arg(range_end)::timestamptz AND range_end>sqlc.arg(range_start)::timestamptz
   AND range_end<=sqlc.arg(cutoff)::timestamptz AND last_error IS DISTINCT FROM 'retention_expired'
   AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
 ORDER BY range_start,id LIMIT 1000 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_derived_dirty_ranges d SET attempts=5,lease_until=NULL,
 lease_epoch=d.lease_epoch+1,last_error='retention_expired'
FROM expired e WHERE d.id=e.id;

-- Only source work that was fenced as expired can release its old aggregates.
-- name: DeleteExpiredRetainedSourceHours :execrows
WITH expired AS (
 SELECT h.sli_id,h.measurement_revision,h.hour_start FROM public.almanac_sli_hourly_aggregates h
 WHERE h.hour_start>=sqlc.arg(range_start)::timestamptz AND h.hour_start<sqlc.arg(range_end)::timestamptz
   AND NOT EXISTS (SELECT 1 FROM public.almanac_derived_dirty_ranges w
       WHERE w.sli_id=h.sli_id AND w.measurement_revision=h.measurement_revision
         AND w.range_start<h.hour_start+interval '1 hour' AND w.range_end>h.hour_start
         AND w.last_error IS DISTINCT FROM 'retention_expired')
 ORDER BY h.hour_start LIMIT 1000 FOR UPDATE OF h SKIP LOCKED
)
DELETE FROM public.almanac_sli_hourly_aggregates h USING expired e
WHERE (h.sli_id,h.measurement_revision,h.hour_start)=(e.sli_id,e.measurement_revision,e.hour_start);

-- name: DeleteExpiredRetainedSourceWork :execrows
WITH expired AS (
 SELECT id FROM public.almanac_derived_dirty_ranges
 WHERE range_start>=sqlc.arg(range_start)::timestamptz AND range_start<sqlc.arg(range_end)::timestamptz
   AND last_error='retention_expired' AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
 ORDER BY id LIMIT 1000 FOR UPDATE SKIP LOCKED
)
DELETE FROM public.almanac_derived_dirty_ranges d USING expired e WHERE d.id=e.id;

-- name: AdvanceRetentionCleanup :execrows
UPDATE public.almanac_retention_work SET state='previewed',attempts=0,lease_until=NULL,last_error=NULL,
 canonical_applied_at=CASE WHEN sqlc.arg(canonical_applied)::boolean THEN coalesce(canonical_applied_at,sqlc.arg(now)::timestamptz) ELSE canonical_applied_at END,
 cleanup_counts=sqlc.arg(cleanup_counts)::jsonb,next_attempt_at=sqlc.arg(now)::timestamptz
WHERE id=sqlc.arg(id) AND lease_epoch=sqlc.arg(lease_epoch) AND state IN ('previewed','blocked');

-- name: HasCalculatedRetentionRows :one
SELECT (EXISTS (SELECT 1 FROM public.almanac_slo_extrema WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_sli_hourly_aggregates WHERE hour_start>=sqlc.arg(range_start)::timestamptz AND hour_start<sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_derived_dirty_ranges WHERE range_start>=sqlc.arg(range_start)::timestamptz AND range_start<sqlc.arg(range_end)::timestamptz))::boolean AS remaining;
