-- A dirty tile or a cursor that has not repaired its endpoints is unavailable,
-- rather than silently serving values from before a late observation arrived.
-- Materialize UTF-8 once for both the byte budget and binary wire payload.
-- name: ReadRetainedParts :many
WITH requested AS (
    SELECT generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(starts)::timestamptz[]) AS range_start,
           unnest(sqlc.arg(ends)::timestamptz[]) AS range_end,
           unnest(sqlc.arg(resolutions)::integer[]) AS resolution
), selected AS MATERIALIZED (
SELECT r.ordinal,r.range_start::timestamptz AS range_start,r.range_end::timestamptz AS range_end,
       r.resolution::integer AS resolution,convert_to(t.payload::text,'UTF8') AS payload,
       t.source_generation,t.published_at,
       (EXISTS (
           SELECT 1 FROM public.almanac_slo_series_tile_work w
           WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
             AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.exclusion_revision=0
             AND w.calculation_version=sqlc.arg(calculation_version) AND w.resolution_seconds=r.resolution
             AND w.range_start=r.range_start AND w.attempts>=5
       ) OR EXISTS (
           SELECT 1 FROM public.almanac_slo_series_repair w
           WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
             AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.calculation_version=sqlc.arg(calculation_version)
             AND w.cursor_at<r.range_end AND w.end_at>r.range_start AND w.attempts>=5
       ))::boolean AS exhausted,
       (t.slo_id IS NOT NULL AND NOT EXISTS (
           SELECT 1 FROM public.almanac_slo_series_tile_work w
           WHERE w.slo_id=t.slo_id AND w.definition_revision=t.definition_revision AND w.measurement_revision=t.measurement_revision
             AND w.exclusion_revision=0 AND w.calculation_version=t.calculation_version
             AND w.resolution_seconds=t.resolution_seconds AND w.range_start=t.range_start
       ) AND NOT EXISTS (
           SELECT 1 FROM public.almanac_slo_series_repair w
           WHERE w.slo_id=t.slo_id AND w.definition_revision=t.definition_revision AND w.measurement_revision=t.measurement_revision
             AND w.calculation_version=t.calculation_version AND w.cursor_at<t.range_end AND w.end_at>t.range_start
       ))::boolean AS ready
FROM requested r LEFT JOIN public.almanac_slo_series_tiles t
  ON t.slo_id=sqlc.arg(slo_id) AND t.definition_revision=sqlc.arg(definition_revision)
 AND t.measurement_revision=sqlc.arg(measurement_revision) AND t.exclusion_revision=0
 AND t.calculation_version=sqlc.arg(calculation_version) AND t.resolution_seconds=r.resolution
 AND t.range_start=r.range_start AND t.range_end=r.range_end
), bounded AS (
SELECT *,coalesce(sum(octet_length(payload)) OVER (),0)::bigint AS payload_bytes FROM selected
)
SELECT ordinal,range_start,range_end,resolution,
       CASE WHEN payload_bytes<=20971520 THEN coalesce(payload,'{}'::bytea) ELSE '{}'::bytea END::bytea AS payload,
       payload_bytes,source_generation,published_at,exhausted,ready
FROM bounded ORDER BY ordinal;

-- name: ReadRetainedEdgePoints :many
WITH selected AS (
    SELECT p.evaluation_at,p.payload,
           sum(octet_length(p.payload::text)) OVER ()::bigint AS payload_bytes,
           EXISTS (
               SELECT 1 FROM public.almanac_slo_series_repair w
               WHERE w.slo_id=p.slo_id AND w.definition_revision=p.definition_revision
                 AND w.measurement_revision=p.measurement_revision AND w.calculation_version=p.calculation_version
                 AND w.cursor_at<p.evaluation_at AND w.end_at>=p.evaluation_at
           ) AS repairing,
           EXISTS (
               SELECT 1 FROM public.almanac_slo_series_repair w
               WHERE w.slo_id=p.slo_id AND w.definition_revision=p.definition_revision
                 AND w.measurement_revision=p.measurement_revision AND w.calculation_version=p.calculation_version
                 AND w.cursor_at<p.evaluation_at AND w.end_at>=p.evaluation_at AND w.attempts>=5
           ) AS exhausted
    FROM public.almanac_slo_series_points p
    WHERE p.slo_id=$1 AND p.definition_revision=$2 AND p.measurement_revision=$3
      AND p.exclusion_revision=0 AND p.calculation_version=$4 AND p.evaluation_at>$5 AND p.evaluation_at<=$6
    ORDER BY p.evaluation_at LIMIT 961
)
SELECT evaluation_at,CASE WHEN payload_bytes<=20971520 THEN payload ELSE '{}'::jsonb END::jsonb AS payload,
       payload_bytes,repairing,exhausted FROM selected ORDER BY evaluation_at;
