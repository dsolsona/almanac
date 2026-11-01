-- Per-interval commit generations distinguish genuinely new coverage from facts
-- already included in a historical point, independently of receipt timestamps.
-- name: ReadRetainedLateTotals :many
WITH requested AS (
    SELECT generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,
           unnest(sqlc.arg(ends)::timestamptz[]) AS end_at,
           unnest(sqlc.arg(generations)::bigint[]) AS source_generation
)
SELECT r.ordinal,a.* FROM requested r CROSS JOIN LATERAL (
    SELECT coalesce(sum(v.good_count),0)::numeric::text AS good,
           coalesce(sum(v.total_count),0)::numeric::text AS total,
           count(*) FILTER (WHERE c.outcome='observed') AS observed,
           count(*) FILTER (WHERE c.outcome='complete_empty') AS known_empty,
           count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0) AS zero_traffic,
           min(c.interval_start) FILTER (WHERE c.outcome='observed')::timestamptz AS first_observation,
           max(c.interval_end) FILTER (WHERE c.outcome='observed')::timestamptz AS last_observation,
           max(c.recorded_at)::timestamptz AS latest_receipt
    FROM public.almanac_sli_coverage c
    LEFT JOIN public.almanac_sli_observation_values v
      ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision
     AND v.interval_start>=r.start_at AND v.interval_start<r.end_at
    WHERE c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
      AND c.ingestion_generation>r.source_generation
      AND c.interval_start>=r.start_at AND c.interval_start<r.end_at AND c.interval_end<=r.end_at
) a ORDER BY r.ordinal;
