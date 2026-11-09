-- name: ReadSloBudgetExtremaState :one
WITH cursor AS (
    SELECT w.* FROM public.almanac_slo_extrema_cursor w
    WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
      AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.calculation_version=sqlc.arg(calculation_version)
), hours AS (
    SELECT e.published_at,
           (w.built_through>=e.range_end AND NOT coalesce(w.dirty_from<e.range_end AND w.dirty_to>e.range_start,false)) AS ready
    FROM public.almanac_slo_extrema e LEFT JOIN cursor w ON true
    WHERE e.slo_id=sqlc.arg(slo_id) AND e.definition_revision=sqlc.arg(definition_revision)
      AND e.measurement_revision=sqlc.arg(measurement_revision) AND e.calculation_version=sqlc.arg(calculation_version)
      AND e.exclusion_revision=0 AND e.resolution_seconds=3600
      AND e.range_start>=sqlc.arg(range_start)::timestamptz AND e.range_end<=sqlc.arg(closed_through)::timestamptz
), prior AS (
    SELECT p.payload FROM public.almanac_slo_current_projections p
    JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    WHERE p.slo_id=sqlc.arg(slo_id) AND g.definition_revision=sqlc.arg(definition_revision)
      AND g.measurement_revision=sqlc.arg(measurement_revision) AND g.calculation_version=sqlc.arg(calculation_version)
)
SELECT (SELECT max(published_at) FROM hours)::timestamptz AS published_at,
       (SELECT count(*) FROM hours WHERE ready)::bigint AS ready_hours,
       coalesce((SELECT attempts>=5 FROM cursor),false)::boolean AS failing,
       coalesce((SELECT payload->'budget_sparkline' FROM prior),'[]'::jsonb)::jsonb AS samples,
       coalesce((SELECT payload->'_budget_cache' FROM prior),'null'::jsonb)::jsonb AS cache;
