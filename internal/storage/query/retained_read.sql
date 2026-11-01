-- A dirty tile or a cursor that has not repaired its endpoints is unavailable,
-- rather than silently serving values from before a late observation arrived.
-- name: ReadRetainedParts :many
WITH requested AS (
    SELECT generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(starts)::timestamptz[]) AS range_start,
           unnest(sqlc.arg(ends)::timestamptz[]) AS range_end,
           unnest(sqlc.arg(resolutions)::integer[]) AS resolution
)
SELECT r.ordinal,r.range_start,r.range_end,r.resolution,coalesce(t.payload,'{}'::jsonb)::jsonb AS payload,
       t.source_generation,t.published_at,
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
ORDER BY r.ordinal;
