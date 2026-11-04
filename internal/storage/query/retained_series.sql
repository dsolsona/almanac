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
DO UPDATE SET version=almanac_slo_series_tile_work.version+1,summary_only=false,
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
    ORDER BY summary_only,next_attempt_at,resolution_seconds,range_start DESC,id
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
    (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,point_count,payload,published_at,compact_payload)
SELECT w.slo_id,w.definition_revision,w.measurement_revision,w.calculation_version,w.resolution_seconds,w.range_start,w.range_end,sqlc.arg(source_generation)::bigint,
       sqlc.arg(point_count)::integer,sqlc.arg(payload)::jsonb,sqlc.arg(published_at)::timestamptz,
       CASE WHEN sqlc.arg(compact_payload)::jsonb IS NULL THEN NULL ELSE sqlc.arg(compact_payload)::jsonb ||
         jsonb_build_object('payload_digest',encode(sha256(convert_to(sqlc.arg(payload)::jsonb::text,'UTF8')),'hex')) END
FROM public.almanac_slo_series_tile_work w
WHERE w.id=sqlc.arg(id) AND w.version=sqlc.arg(version) AND w.lease_epoch=sqlc.arg(lease_epoch)
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET source_generation=EXCLUDED.source_generation,point_count=EXCLUDED.point_count,
              payload=EXCLUDED.payload,published_at=EXCLUDED.published_at,compact_payload=EXCLUDED.compact_payload
WHERE almanac_slo_series_tiles.published_at<=EXCLUDED.published_at;

-- name: AcknowledgeRetainedTileWork :execrows
DELETE FROM public.almanac_slo_series_tile_work WHERE id=$1 AND version=$2 AND lease_epoch=$3;

-- name: ReleaseRetainedTileWork :exec
UPDATE public.almanac_slo_series_tile_work SET lease_until=NULL,attempts=0,last_error=NULL
WHERE id=$1 AND lease_epoch=$2;

-- name: QueueRetainedTileSummaries :execrows
WITH candidates AS (
    SELECT t.slo_id,t.definition_revision,t.measurement_revision,t.calculation_version,t.resolution_seconds,t.range_start,t.range_end,t.source_generation
    FROM public.almanac_slo_series_tiles t JOIN public.almanac_slos s ON s.id=t.slo_id AND s.definition_revision=t.definition_revision
    WHERE t.exclusion_revision=0 AND t.calculation_version=sqlc.arg(calculation_version) AND t.range_end>sqlc.arg(retention_start)
      AND (t.compact_payload IS NULL OR t.payload_digest IS NULL
        OR t.compact_payload->>'payload_digest' IS DISTINCT FROM encode(t.payload_digest,'hex')
        OR t.compact_payload->>'source_generation' IS DISTINCT FROM t.source_generation::text
        OR (t.compact_payload ? 'tile' AND NOT t.compact_payload ? 'wire')
        OR (t.resolution_seconds=3600 AND NOT t.compact_payload ? 'fine_wire'))
      AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_series_tile_work w WHERE w.slo_id=t.slo_id
        AND w.definition_revision=t.definition_revision AND w.measurement_revision=t.measurement_revision AND w.exclusion_revision=t.exclusion_revision
        AND w.calculation_version=t.calculation_version AND w.resolution_seconds=t.resolution_seconds AND w.range_start=t.range_start)
    ORDER BY t.resolution_seconds DESC,t.range_end DESC,t.slo_id LIMIT 100
)
INSERT INTO public.almanac_slo_series_tile_work
  (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,next_attempt_at,summary_only)
SELECT slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,sqlc.arg(now),true FROM candidates
ON CONFLICT DO NOTHING;

-- name: ReadRetainedTileSummaryInput :one
SELECT t.payload,t.source_generation
FROM public.almanac_slo_series_tiles t JOIN public.almanac_slo_series_tile_work w
  ON w.slo_id=t.slo_id AND w.definition_revision=t.definition_revision AND w.measurement_revision=t.measurement_revision
 AND w.exclusion_revision=t.exclusion_revision AND w.calculation_version=t.calculation_version
 AND w.resolution_seconds=t.resolution_seconds AND w.range_start=t.range_start
WHERE w.id=$1;

-- name: PublishRetainedTileSummary :execrows
UPDATE public.almanac_slo_series_tiles t
SET payload_digest=sha256(convert_to(t.payload::text,'UTF8')),
    compact_payload=sqlc.arg(compact_payload)::jsonb || jsonb_build_object('payload_digest',encode(sha256(convert_to(t.payload::text,'UTF8')),'hex'))
FROM public.almanac_slo_series_tile_work w
WHERE w.id=sqlc.arg(id) AND w.version=sqlc.arg(version) AND w.lease_epoch=sqlc.arg(lease_epoch) AND w.summary_only
  AND t.slo_id=w.slo_id AND t.definition_revision=w.definition_revision AND t.measurement_revision=w.measurement_revision
  AND t.exclusion_revision=w.exclusion_revision AND t.calculation_version=w.calculation_version
  AND t.resolution_seconds=w.resolution_seconds AND t.range_start=w.range_start AND t.source_generation=sqlc.arg(source_generation);

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

