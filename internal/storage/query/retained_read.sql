-- A repair cursor is scheduling metadata, not proof that every later point is
-- invalid. Reuse the source-hour watermark checks used by display blocks.
-- Materialize UTF-8 once for both the byte budget and binary wire payload.
-- name: ReadRetainedParts :many
WITH objective AS (
    SELECT definition.sli_id,GREATEST(definition.rolling_window_seconds,21600)::bigint AS window_seconds
    FROM public.almanac_slo_definitions definition
    WHERE definition.slo_id=sqlc.arg(slo_id) AND definition.revision=sqlc.arg(definition_revision)
), requested AS (
    SELECT generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(starts)::timestamptz[]) AS range_start,
           unnest(sqlc.arg(ends)::timestamptz[]) AS range_end,
           unnest(sqlc.arg(resolutions)::integer[]) AS resolution
), selected AS MATERIALIZED (
SELECT r.ordinal,r.range_start::timestamptz AS range_start,r.range_end::timestamptz AS range_end,
       r.resolution::integer AS resolution,convert_to(t.payload::text,'UTF8') AS payload,
       t.source_generation,t.published_at,t.point_count,
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
           SELECT 1 FROM public.almanac_derived_dirty_ranges d
           WHERE d.sli_id=o.sli_id AND d.measurement_revision=t.measurement_revision
             AND d.generation>t.source_generation
             AND d.range_start>=(date_trunc('hour',(t.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
             AND d.range_start<t.range_end AND d.range_end>t.range_start-o.window_seconds*interval '1 second'
       ) AND NOT EXISTS (
           SELECT 1 FROM public.almanac_sli_hourly_aggregates a
           WHERE a.sli_id=o.sli_id AND a.measurement_revision=t.measurement_revision
             AND a.source_generation>t.source_generation
             AND a.hour_start>=(date_trunc('hour',(t.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
             AND a.hour_start<t.range_end
       ))::boolean AS ready
FROM requested r LEFT JOIN public.almanac_slo_series_tiles t
  ON t.slo_id=sqlc.arg(slo_id) AND t.definition_revision=sqlc.arg(definition_revision)
 AND t.measurement_revision=sqlc.arg(measurement_revision) AND t.exclusion_revision=0
 AND t.calculation_version=sqlc.arg(calculation_version) AND t.resolution_seconds=r.resolution
 AND t.range_start=r.range_start AND t.range_end=r.range_end
CROSS JOIN objective o
), bounded AS (
SELECT *,coalesce(sum(octet_length(payload)) OVER (),0)::bigint AS payload_bytes FROM selected
)
SELECT ordinal,range_start,range_end,resolution,
       CASE WHEN payload_bytes<=20971520 THEN coalesce(payload,'{}'::bytea) ELSE '{}'::bytea END::bytea AS payload,
       payload_bytes,source_generation,published_at,point_count,exhausted,ready
FROM bounded ORDER BY ordinal;

-- name: ReadRetainedEdgePoints :many
WITH objective AS (
    SELECT definition.sli_id,GREATEST(definition.rolling_window_seconds,21600)::bigint AS window_seconds
    FROM public.almanac_slo_definitions definition WHERE definition.slo_id=sqlc.arg(slo_id) AND definition.revision=sqlc.arg(definition_revision)
), selected AS (
    SELECT p.evaluation_at,p.payload,
           sum(octet_length(p.payload::text)) OVER ()::bigint AS payload_bytes,
           (EXISTS (
               SELECT 1 FROM public.almanac_derived_dirty_ranges d
               WHERE d.sli_id=o.sli_id AND d.measurement_revision=p.measurement_revision
                 AND d.generation>p.source_generation
                 AND d.range_start>=(date_trunc('hour',(p.evaluation_at-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
                 AND d.range_start<p.evaluation_at AND d.range_end>p.evaluation_at-o.window_seconds*interval '1 second'
           ) OR EXISTS (
               SELECT 1 FROM public.almanac_sli_hourly_aggregates a
               WHERE a.sli_id=o.sli_id AND a.measurement_revision=p.measurement_revision
                 AND a.source_generation>p.source_generation
                 AND a.hour_start>=(date_trunc('hour',(p.evaluation_at-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
                 AND a.hour_start<p.evaluation_at
           ))::boolean AS repairing,
           EXISTS (
               SELECT 1 FROM public.almanac_slo_series_repair w
               WHERE w.slo_id=p.slo_id AND w.definition_revision=p.definition_revision
                 AND w.measurement_revision=p.measurement_revision AND w.calculation_version=p.calculation_version
                 AND w.cursor_at<p.evaluation_at AND w.end_at>=p.evaluation_at AND w.attempts>=5
           ) AS exhausted
    FROM public.almanac_slo_series_points p CROSS JOIN objective o
    WHERE p.slo_id=sqlc.arg(slo_id) AND p.definition_revision=sqlc.arg(definition_revision) AND p.measurement_revision=sqlc.arg(measurement_revision)
      AND p.exclusion_revision=0 AND p.calculation_version=sqlc.arg(calculation_version) AND p.evaluation_at>sqlc.arg(evaluation_at) AND p.evaluation_at<=sqlc.arg(evaluation_at_2)
    ORDER BY p.evaluation_at LIMIT 961
)
SELECT evaluation_at,CASE WHEN payload_bytes<=20971520 THEN payload ELSE '{}'::jsonb END::jsonb AS payload,
       payload_bytes,repairing,exhausted FROM selected ORDER BY evaluation_at;
