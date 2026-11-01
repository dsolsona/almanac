-- Adjacent evaluation endpoints share whole-hour interiors. Aggregate each distinct
-- interior once; only cadence-aligned boundary fragments use canonical rows.
-- name: ReadRetainedWindowBatch :many
WITH requested AS MATERIALIZED (
    SELECT generate_series(1,cardinality(sqlc.arg(window_ends)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(window_ends)::timestamptz[]) AS window_end,
           unnest(sqlc.arg(full_starts)::timestamptz[]) AS full_start,
           unnest(sqlc.arg(full_ends)::timestamptz[]) AS full_end,
           unnest(sqlc.arg(edge_a_starts)::timestamptz[]) AS edge_a_start,
           unnest(sqlc.arg(edge_a_ends)::timestamptz[]) AS edge_a_end,
           unnest(sqlc.arg(edge_b_starts)::timestamptz[]) AS edge_b_start,
           unnest(sqlc.arg(edge_b_ends)::timestamptz[]) AS edge_b_end
), interiors AS MATERIALIZED (
    SELECT DISTINCT full_start,full_end FROM requested
), totals AS MATERIALIZED (
    SELECT i.full_start,i.full_end,a.*,
           NOT EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d
               WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
                 AND i.full_start<i.full_end AND d.range_start<i.full_end AND d.range_end>i.full_start) AS prepared,
           EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d
               WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
                 AND i.full_start<i.full_end AND d.range_start<i.full_end AND d.range_end>i.full_start
                 AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)) AS exhausted
    FROM interiors i CROSS JOIN LATERAL (
        SELECT coalesce(sum(h.good),0)::numeric AS good,coalesce(sum(h.total),0)::numeric AS total,
               coalesce(sum(h.observed_intervals),0)::bigint AS observed,
               coalesce(sum(h.known_empty_intervals),0)::bigint AS known_empty,
               coalesce(sum(h.zero_traffic_intervals),0)::bigint AS zero_traffic,
               min(h.first_observation_at)::timestamptz AS first_observation,
               max(h.last_observation_at)::timestamptz AS last_observation,
               max(h.latest_receipt_at)::timestamptz AS latest_receipt
        FROM public.almanac_sli_hourly_aggregates h
        WHERE h.sli_id=sqlc.arg(sli_id) AND h.measurement_revision=sqlc.arg(measurement_revision)
          AND h.hour_start>=i.full_start AND h.hour_start<i.full_end
    ) a
)
SELECT r.ordinal,(t.good+e.good)::numeric::text AS good,(t.total+e.total)::numeric::text AS total,
       (t.observed+e.observed)::bigint AS observed,(t.known_empty+e.known_empty)::bigint AS known_empty,
       (t.zero_traffic+e.zero_traffic)::bigint AS zero_traffic,
       LEAST(t.first_observation,e.first_observation)::timestamptz AS first_observation,
       GREATEST(t.last_observation,e.last_observation)::timestamptz AS last_observation,
       GREATEST(t.latest_receipt,e.latest_receipt)::timestamptz AS latest_receipt,t.prepared,t.exhausted
FROM requested r JOIN totals t ON t.full_start=r.full_start AND t.full_end=r.full_end
CROSS JOIN LATERAL (
    SELECT coalesce(sum(v.good_count),0)::numeric AS good,coalesce(sum(v.total_count),0)::numeric AS total,
           count(*) FILTER (WHERE c.outcome='observed') AS observed,
           count(*) FILTER (WHERE c.outcome='complete_empty') AS known_empty,
           count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0) AS zero_traffic,
           min(c.interval_start) FILTER (WHERE c.outcome='observed') AS first_observation,
           max(c.interval_end) FILTER (WHERE c.outcome='observed') AS last_observation,max(c.recorded_at) AS latest_receipt
    FROM public.almanac_sli_coverage c
    LEFT JOIN LATERAL (
        SELECT value.good_count,value.total_count FROM public.almanac_sli_observation_values value
        WHERE value.interval_start=c.interval_start AND value.sli_id=sqlc.arg(sli_id)
          AND value.measurement_revision=sqlc.arg(measurement_revision)
          AND value.interval_start>=LEAST(r.edge_a_start,r.edge_b_start) AND value.interval_start<r.window_end
        LIMIT 1
    ) v ON true
    WHERE c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
      AND ((c.interval_start>=r.edge_a_start AND c.interval_start<r.edge_a_end)
        OR (c.interval_start>=r.edge_b_start AND c.interval_start<r.edge_b_end))
      AND c.interval_end<=r.window_end
) e ORDER BY r.ordinal;
