-- name: PublishRetainedSloPoint :execrows
WITH accepted AS (
INSERT INTO public.almanac_slo_series_points
    (slo_id,definition_revision,measurement_revision,calculation_version,evaluation_at,
     source_generation,published_at,payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,evaluation_at)
DO UPDATE SET source_generation=EXCLUDED.source_generation,published_at=EXCLUDED.published_at,payload=EXCLUDED.payload
WHERE almanac_slo_series_points.source_generation<EXCLUDED.source_generation
RETURNING slo_id,definition_revision,measurement_revision,calculation_version,evaluation_at,source_generation,published_at
), resolutions AS (
    SELECT 3600::integer AS seconds UNION ALL SELECT 86400::integer
), wanted AS (
    SELECT a.*,r.seconds,
           to_timestamp(floor((extract(epoch FROM a.evaluation_at)-0.000001)/r.seconds)*r.seconds) AS start_at
    FROM accepted a CROSS JOIN resolutions r
)
INSERT INTO public.almanac_slo_series_tile_work
    (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,next_attempt_at)
SELECT slo_id,definition_revision,measurement_revision,calculation_version,seconds,start_at,start_at+seconds*interval '1 second',source_generation,
       CASE WHEN seconds=86400 THEN GREATEST(published_at,start_at+interval '1 day') ELSE published_at END
FROM wanted
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET version=almanac_slo_series_tile_work.version+1,
              source_generation=GREATEST(almanac_slo_series_tile_work.source_generation,EXCLUDED.source_generation),
              next_attempt_at=LEAST(almanac_slo_series_tile_work.next_attempt_at,EXCLUDED.next_attempt_at);

-- name: ReadRetainedSloPoint :one
SELECT source_generation,published_at,payload
FROM public.almanac_slo_series_points
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3
  AND exclusion_revision=0 AND calculation_version=$4 AND evaluation_at=$5;

-- name: ClaimRetainedTileWork :one
WITH candidate AS (
    SELECT id FROM public.almanac_slo_series_tile_work
    WHERE attempts<5 AND next_attempt_at<=sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY next_attempt_at,resolution_seconds,range_start DESC,id
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_slo_series_tile_work w
SET attempts=w.attempts+1,lease_epoch=w.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz
FROM candidate c WHERE w.id=c.id RETURNING w.*;

-- name: ReadRetainedTilePoints :many
SELECT evaluation_at,source_generation,payload
FROM public.almanac_slo_series_points
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3
  AND exclusion_revision=0 AND calculation_version=$4 AND evaluation_at>$5 AND evaluation_at<=$6
ORDER BY evaluation_at LIMIT 5761;

-- name: PublishRetainedTile :execrows
INSERT INTO public.almanac_slo_series_tiles
    (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,point_count,payload,published_at)
SELECT w.slo_id,w.definition_revision,w.measurement_revision,w.calculation_version,w.resolution_seconds,w.range_start,w.range_end,w.source_generation,
       sqlc.arg(point_count)::integer,sqlc.arg(payload)::jsonb,sqlc.arg(published_at)::timestamptz
FROM public.almanac_slo_series_tile_work w
WHERE w.id=sqlc.arg(id) AND w.version=sqlc.arg(version) AND w.lease_epoch=sqlc.arg(lease_epoch)
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET source_generation=EXCLUDED.source_generation,point_count=EXCLUDED.point_count,
              payload=EXCLUDED.payload,published_at=EXCLUDED.published_at
WHERE almanac_slo_series_tiles.published_at<=EXCLUDED.published_at;

-- name: AcknowledgeRetainedTileWork :execrows
DELETE FROM public.almanac_slo_series_tile_work WHERE id=$1 AND version=$2 AND lease_epoch=$3;

-- name: ReleaseRetainedTileWork :exec
UPDATE public.almanac_slo_series_tile_work SET lease_until=NULL,attempts=0,last_error=NULL
WHERE id=$1 AND lease_epoch=$2;

-- name: ReadRetainedSeriesTiles :many
SELECT range_start,range_end,source_generation,point_count,payload,published_at
FROM public.almanac_slo_series_tiles
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND exclusion_revision=0
  AND calculation_version=$4 AND resolution_seconds=$5 AND range_start >= $6 AND range_end <= $7
ORDER BY range_start LIMIT 1000;

-- name: FailRetainedTileWork :exec
UPDATE public.almanac_slo_series_tile_work SET lease_until=NULL,
    next_attempt_at=$3::timestamptz+power(2,LEAST(attempts,5))*interval '1 second',last_error=$4
WHERE id=$1 AND lease_epoch=$2;

-- name: DeferRetainedTileWork :exec
UPDATE public.almanac_slo_series_tile_work SET lease_until=NULL,attempts=GREATEST(attempts-1,0),next_attempt_at=$3
WHERE id=$1 AND lease_epoch=$2;

