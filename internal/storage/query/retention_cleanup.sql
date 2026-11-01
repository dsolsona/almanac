-- Preview counts stop at 1001; the report labels these as lower bounds.
-- name: PreviewCalculatedRetention :one
SELECT
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_slo_series_points WHERE evaluation_at>sqlc.arg(range_start)::timestamptz AND evaluation_at<=sqlc.arg(range_end)::timestamptz LIMIT 1001) p)::bigint AS points,
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_slo_series_tiles WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz LIMIT 1001) t)::bigint AS tiles,
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_slo_series_tile_work WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz LIMIT 1001) w)::bigint AS tile_work,
 (SELECT count(*) FROM (SELECT 1 FROM public.almanac_sli_hourly_aggregates WHERE hour_start>=sqlc.arg(range_start)::timestamptz AND hour_start<sqlc.arg(range_end)::timestamptz LIMIT 1001) h)::bigint AS source_hours;

-- Expired leases are fenced before removing their facts. Retained failure rows
-- remain inspectable and cannot be manually retried against deleted inputs.
-- name: ExpireRetentionHistoryJobs :execrows
WITH expired AS (
 SELECT id FROM public.almanac_slo_history_jobs
 WHERE retention_start<sqlc.arg(range_end)::timestamptz
   AND greatest(range_end,evaluated_through)>sqlc.arg(range_start)::timestamptz
   AND state<>'failed' AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
 ORDER BY id LIMIT 1000 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_slo_history_jobs j SET state='failed',attempts=5,lease_until=NULL,
 lease_epoch=j.lease_epoch+1,last_error='retention_expired'
FROM expired e WHERE j.id=e.id;

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

-- A repair cursor's expired prefix can be skipped, but its retained tail stays
-- admitted. Bumping both fences prevents an old lease from moving it backwards.
-- name: SkipExpiredRetainedRepairs :execrows
WITH expired AS (
 SELECT w.id,LEAST(w.end_at,to_timestamp(ceil(extract(epoch FROM sqlc.arg(cutoff)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)) AS through
 FROM public.almanac_slo_series_repair w
 JOIN public.almanac_slo_definitions d ON d.slo_id=w.slo_id AND d.revision=w.definition_revision
 WHERE w.cursor_at<sqlc.arg(range_end)::timestamptz AND w.cursor_at<w.end_at
   AND (w.lease_until IS NULL OR w.lease_until<=sqlc.arg(now)::timestamptz)
 ORDER BY w.id LIMIT 1000 FOR UPDATE OF w SKIP LOCKED
)
UPDATE public.almanac_slo_series_repair w SET cursor_at=e.through,lease_until=NULL,
 version=w.version+1,lease_epoch=w.lease_epoch+1,last_error='retention_expired'
FROM expired e WHERE w.id=e.id AND w.cursor_at<e.through;

-- name: DeleteExpiredRetainedTileWork :execrows
WITH expired AS (
 SELECT id FROM public.almanac_slo_series_tile_work
 WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz
   AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
 ORDER BY id LIMIT 1000 FOR UPDATE SKIP LOCKED
)
DELETE FROM public.almanac_slo_series_tile_work w USING expired e WHERE w.id=e.id;

-- name: DeleteExpiredRetainedPoints :execrows
WITH expired AS (
 SELECT p.slo_id,p.definition_revision,p.measurement_revision,p.exclusion_revision,p.calculation_version,p.evaluation_at
 FROM public.almanac_slo_series_points p
 WHERE p.evaluation_at>sqlc.arg(range_start)::timestamptz AND p.evaluation_at<=sqlc.arg(range_end)::timestamptz
   AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_series_tile_work w
       WHERE w.slo_id=p.slo_id AND w.definition_revision=p.definition_revision AND w.measurement_revision=p.measurement_revision
         AND w.exclusion_revision=p.exclusion_revision AND w.calculation_version=p.calculation_version
         AND p.evaluation_at>w.range_start AND p.evaluation_at<=w.range_end)
   AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_series_repair w
       WHERE w.slo_id=p.slo_id AND w.definition_revision=p.definition_revision AND w.measurement_revision=p.measurement_revision
         AND w.calculation_version=p.calculation_version AND w.cursor_at<p.evaluation_at AND w.end_at>=p.evaluation_at)
 ORDER BY p.evaluation_at LIMIT 1000 FOR UPDATE OF p SKIP LOCKED
)
DELETE FROM public.almanac_slo_series_points p USING expired e
WHERE (p.slo_id,p.definition_revision,p.measurement_revision,p.exclusion_revision,p.calculation_version,p.evaluation_at)
    =(e.slo_id,e.definition_revision,e.measurement_revision,e.exclusion_revision,e.calculation_version,e.evaluation_at);

-- name: DeleteExpiredRetainedTiles :execrows
WITH expired AS (
 SELECT t.slo_id,t.definition_revision,t.measurement_revision,t.exclusion_revision,t.calculation_version,t.resolution_seconds,t.range_start
 FROM public.almanac_slo_series_tiles t
 WHERE t.range_end>sqlc.arg(range_start)::timestamptz AND t.range_end<=sqlc.arg(range_end)::timestamptz
   AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_series_tile_work w
       WHERE w.slo_id=t.slo_id AND w.definition_revision=t.definition_revision AND w.measurement_revision=t.measurement_revision
         AND w.exclusion_revision=t.exclusion_revision AND w.calculation_version=t.calculation_version
         AND w.resolution_seconds=t.resolution_seconds AND w.range_start=t.range_start)
 ORDER BY t.range_end LIMIT 1000 FOR UPDATE OF t SKIP LOCKED
)
DELETE FROM public.almanac_slo_series_tiles t USING expired e
WHERE (t.slo_id,t.definition_revision,t.measurement_revision,t.exclusion_revision,t.calculation_version,t.resolution_seconds,t.range_start)
    =(e.slo_id,e.definition_revision,e.measurement_revision,e.exclusion_revision,e.calculation_version,e.resolution_seconds,e.range_start);

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

-- name: HasCalculatedRetentionRows :one
SELECT (EXISTS (SELECT 1 FROM public.almanac_slo_series_points WHERE evaluation_at>sqlc.arg(range_start)::timestamptz AND evaluation_at<=sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_slo_series_tiles WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_slo_series_tile_work WHERE range_end>sqlc.arg(range_start)::timestamptz AND range_end<=sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_sli_hourly_aggregates WHERE hour_start>=sqlc.arg(range_start)::timestamptz AND hour_start<sqlc.arg(range_end)::timestamptz)
 OR EXISTS (SELECT 1 FROM public.almanac_derived_dirty_ranges WHERE range_start>=sqlc.arg(range_start)::timestamptz AND range_start<sqlc.arg(range_end)::timestamptz))::boolean AS remaining;

-- name: AdvanceRetentionCleanup :execrows
UPDATE public.almanac_retention_work SET state='previewed',attempts=0,lease_until=NULL,last_error=NULL,
 canonical_applied_at=CASE WHEN sqlc.arg(canonical_applied)::boolean THEN coalesce(canonical_applied_at,sqlc.arg(now)::timestamptz) ELSE canonical_applied_at END,
 cleanup_counts=sqlc.arg(cleanup_counts)::jsonb,next_attempt_at=sqlc.arg(now)::timestamptz
WHERE id=sqlc.arg(id) AND lease_epoch=sqlc.arg(lease_epoch) AND state IN ('previewed','blocked');
