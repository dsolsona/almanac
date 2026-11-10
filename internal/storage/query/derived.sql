-- name: GetDerivedSourceGeneration :one
SELECT generation FROM public.almanac_sli_source_generations
WHERE sli_id=$1 AND measurement_revision=$2;

-- name: ReadDerivedWindowTotals :one
WITH measurement AS (
    SELECT datasource.polling_interval_seconds
    FROM public.almanac_sli_measurements m
    JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
    WHERE m.sli_id=sqlc.arg(sli_id) AND m.revision=sqlc.arg(measurement_revision)
), readiness AS (
    SELECT NOT EXISTS (
        SELECT 1 FROM public.almanac_derived_dirty_ranges d
        WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
          AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
          AND d.range_start<sqlc.arg(full_end)::timestamptz
          AND d.range_end>sqlc.arg(full_start)::timestamptz
    ) AS prepared,EXISTS (
        SELECT 1 FROM public.almanac_derived_dirty_ranges d
        WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
          AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
          AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
          AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
    ) AS exhausted
), raw_ranges AS (
    SELECT unnest(sqlc.arg(edge_starts)::timestamptz[]) AS start_at,
           unnest(sqlc.arg(edge_ends)::timestamptz[]) AS end_at
), contributions AS (
    SELECT a.good, a.total, a.observed_intervals AS observed,
           a.known_empty_intervals AS known_empty, a.zero_traffic_intervals AS zero_traffic,
           a.first_observation_at AS first_observation, a.last_observation_at AS last_observation,
           a.latest_receipt_at AS latest_receipt
    FROM public.almanac_sli_hourly_aggregates a
    WHERE a.sli_id=sqlc.arg(sli_id) AND a.measurement_revision=sqlc.arg(measurement_revision)
      AND a.hour_start >= sqlc.arg(full_start)::timestamptz
      AND a.hour_start < sqlc.arg(full_end)::timestamptz
      AND (SELECT prepared FROM readiness)
    UNION ALL
    SELECT coalesce(sum(c.good_count),0)::numeric, coalesce(sum(c.total_count),0)::numeric,
           count(*) FILTER (WHERE c.outcome='observed'),
           count(*) FILTER (WHERE c.outcome='complete_empty'),
           count(*) FILTER (WHERE c.outcome='observed' AND c.total_count=0),
           min(c.interval_start) FILTER (WHERE c.outcome='observed'),
           max(c.interval_start+m.polling_interval_seconds*interval '1 second') FILTER (WHERE c.outcome='observed'), max(c.recorded_at)
    FROM raw_ranges r JOIN public.almanac_sli_observations c
      ON c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
      AND c.interval_start >= r.start_at AND c.interval_start < r.end_at
    CROSS JOIN measurement m
    WHERE c.interval_start+m.polling_interval_seconds*interval '1 second'<=sqlc.arg(window_end)::timestamptz
      AND (SELECT prepared FROM readiness)
)
SELECT coalesce(sum(good),0)::numeric::text AS good,
       coalesce(sum(total),0)::numeric::text AS total,
       coalesce(sum(observed),0)::bigint AS observed,
       coalesce(sum(known_empty),0)::bigint AS known_empty,
       coalesce(sum(zero_traffic),0)::bigint AS zero_traffic,
       min(first_observation)::timestamptz AS first_observation,
       max(last_observation)::timestamptz AS last_observation,
       max(latest_receipt)::timestamptz AS latest_receipt,
       (SELECT prepared FROM readiness)::boolean AS prepared,
       (SELECT exhausted FROM readiness)::boolean AS exhausted
FROM contributions;

-- name: HasExhaustedDerivedSource :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
      AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
      AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
);
