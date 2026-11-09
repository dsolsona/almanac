-- name: ClaimSloExtremaCursor :one
WITH candidate AS (
    SELECT w.id FROM public.almanac_slo_extrema_cursor w
    JOIN public.almanac_slos s ON s.id=w.slo_id AND s.definition_revision=w.definition_revision
    WHERE w.calculation_version=sqlc.arg(calculation_version)
      AND (w.built_through<sqlc.arg(closed_through)::timestamptz OR w.dirty_from<LEAST(w.dirty_to,sqlc.arg(closed_through)::timestamptz))
      AND (w.attempts<5 OR w.failing_since IS NULL OR w.failing_since>sqlc.arg(now)::timestamptz-interval '7 days')
      AND w.next_attempt_at<=sqlc.arg(now)::timestamptz AND (w.lease_until IS NULL OR w.lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY w.next_attempt_at,w.slo_id LIMIT 1 FOR UPDATE OF w SKIP LOCKED
)
UPDATE public.almanac_slo_extrema_cursor w
SET attempts=LEAST(w.attempts+1,5),lease_epoch=w.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz
FROM candidate c WHERE w.id=c.id RETURNING w.*;

-- name: PublishSloExtrema :execrows
INSERT INTO public.almanac_slo_extrema
    (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT w.slo_id,w.definition_revision,w.measurement_revision,w.calculation_version,
       sqlc.arg(resolution_seconds),sqlc.arg(range_start)::timestamptz,sqlc.arg(range_end)::timestamptz,
       sqlc.arg(source_generation),sqlc.arg(retention_start)::timestamptz,sqlc.arg(published_at)::timestamptz,sqlc.arg(lanes)::jsonb
FROM public.almanac_slo_extrema_cursor w
JOIN public.almanac_slos s ON s.id=w.slo_id AND s.definition_revision=w.definition_revision
WHERE w.id=sqlc.arg(id) AND w.version=sqlc.arg(version) AND w.lease_epoch=sqlc.arg(lease_epoch)
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET range_end=EXCLUDED.range_end,source_generation=EXCLUDED.source_generation,
              retention_start=EXCLUDED.retention_start,published_at=EXCLUDED.published_at,lanes=EXCLUDED.lanes
WHERE almanac_slo_extrema.source_generation<=EXCLUDED.source_generation;

-- name: AdvanceSloExtremaCursor :execrows
UPDATE public.almanac_slo_extrema_cursor
SET built_through=sqlc.arg(built_through)::timestamptz,dirty_from=sqlc.narg(dirty_from)::timestamptz,dirty_to=sqlc.narg(dirty_to)::timestamptz,
    attempts=0,lease_until=NULL,last_error=NULL,failing_since=NULL,next_attempt_at=sqlc.arg(next_attempt_at)::timestamptz
WHERE id=sqlc.arg(id) AND version=sqlc.arg(version) AND lease_epoch=sqlc.arg(lease_epoch);

-- name: DeferSloExtremaCursor :exec
UPDATE public.almanac_slo_extrema_cursor SET lease_until=NULL,
    attempts=CASE WHEN attempts>=5 AND failing_since IS NOT NULL THEN attempts ELSE GREATEST(attempts-1,0) END,
    next_attempt_at=sqlc.arg(next_attempt_at)::timestamptz,last_error=sqlc.narg(reason)::text
WHERE id=sqlc.arg(id) AND lease_epoch=sqlc.arg(lease_epoch) AND version=sqlc.arg(version);

-- name: FailSloExtremaCursor :exec
UPDATE public.almanac_slo_extrema_cursor SET lease_until=NULL,
    next_attempt_at=sqlc.arg(now)::timestamptz+CASE WHEN attempts<5 THEN power(2,attempts)*interval '1 second'
        WHEN coalesce(failing_since,sqlc.arg(now)::timestamptz)>sqlc.arg(now)::timestamptz-interval '24 hours' THEN interval '5 minutes' ELSE interval '1 hour' END,
    failing_since=coalesce(failing_since,sqlc.arg(now)::timestamptz),last_error=sqlc.arg(reason)::text
WHERE id=sqlc.arg(id) AND lease_epoch=sqlc.arg(lease_epoch) AND version=sqlc.arg(version);

-- name: ReadSloExtremaHours :many
SELECT * FROM public.almanac_slo_extrema
WHERE slo_id=sqlc.arg(slo_id) AND definition_revision=sqlc.arg(definition_revision)
  AND measurement_revision=sqlc.arg(measurement_revision) AND calculation_version=sqlc.arg(calculation_version)
  AND exclusion_revision=0 AND resolution_seconds=3600 AND range_start>=sqlc.arg(range_start)::timestamptz
  AND range_end<=sqlc.arg(range_end)::timestamptz ORDER BY range_start;

-- name: ReadSloExtremaParts :many
WITH requested AS (
    SELECT unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,unnest(sqlc.arg(ends)::timestamptz[]) AS end_at,
           unnest(sqlc.arg(resolutions)::integer[]) AS resolution,generate_subscripts(sqlc.arg(starts)::timestamptz[],1)::integer AS ordinal
)
SELECT r.ordinal,r.start_at,r.end_at,r.resolution,e.retention_start,e.source_generation,e.published_at,e.lanes,
       (e.slo_id IS NOT NULL AND NOT coalesce(w.dirty_from<r.end_at AND w.dirty_to>r.start_at,false))::boolean AS ready,
       coalesce(w.attempts>=5,false)::boolean AS failing,
       coalesce(sum(octet_length(e.lanes::text)) OVER (),0)::bigint AS payload_bytes
FROM requested r
LEFT JOIN public.almanac_slo_extrema e ON e.slo_id=sqlc.arg(slo_id) AND e.definition_revision=sqlc.arg(definition_revision)
  AND e.measurement_revision=sqlc.arg(measurement_revision) AND e.calculation_version=sqlc.arg(calculation_version)
  AND e.exclusion_revision=0 AND e.resolution_seconds=r.resolution AND e.range_start=r.start_at AND e.range_end=r.end_at
LEFT JOIN public.almanac_slo_extrema_cursor w ON w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
  AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.calculation_version=sqlc.arg(calculation_version)
ORDER BY r.ordinal;

-- name: DeleteExpiredSloExtrema :execrows
DELETE FROM public.almanac_slo_extrema e WHERE
    (e.slo_id,e.definition_revision,e.measurement_revision,e.exclusion_revision,e.calculation_version,e.resolution_seconds,e.range_start) IN (
    SELECT slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start
    FROM public.almanac_slo_extrema WHERE range_end<=sqlc.arg(cutoff)::timestamptz ORDER BY range_end LIMIT 500
    FOR UPDATE SKIP LOCKED
);
