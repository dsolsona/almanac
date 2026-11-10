-- Adjacent evaluation endpoints share whole-hour interiors. Aggregate each distinct
-- interior once; only cadence-aligned boundary fragments use canonical rows.
-- name: ReadRetainedWindowBatch :many
WITH measurement AS MATERIALIZED (
    SELECT datasource.polling_interval_seconds
    FROM public.almanac_sli_measurements m
    JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
    WHERE m.sli_id=sqlc.arg(sli_id) AND m.revision=sqlc.arg(measurement_revision)
), requested AS MATERIALIZED (
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
), edge_ranges AS MATERIALIZED (
    SELECT DISTINCT edge_a_start AS start_at,edge_a_end AS end_at FROM requested WHERE edge_a_start<edge_a_end
    UNION
    SELECT DISTINCT edge_b_start,edge_b_end FROM requested WHERE edge_b_start<edge_b_end
), edges AS MATERIALIZED (
    -- Array position does not identify a prefix: when the leading fragment is
    -- empty, the trailing fragment occupies the first slot. Share the more
    -- common boundary instead, including windows clamped by validity/retention.
    SELECT start_at,end_at,
           CASE WHEN count(*) OVER (PARTITION BY start_at)>count(*) OVER (PARTITION BY end_at) THEN 'b' ELSE 'a' END AS lane,
           CASE WHEN count(*) OVER (PARTITION BY start_at)>count(*) OVER (PARTITION BY end_at) THEN start_at ELSE end_at END AS anchor_at
    FROM edge_ranges
), anchors AS MATERIALIZED (
    SELECT lane,anchor_at,min(start_at)::timestamptz AS start_at,max(end_at)::timestamptz AS end_at
    FROM edges GROUP BY lane,anchor_at
), edge_input AS (
    SELECT a.lane,a.start_at AS anchor_start,a.end_at AS anchor_end,
           CASE WHEN a.lane='a' THEN -extract(epoch FROM c.interval_start) ELSE extract(epoch FROM c.interval_end) END AS sort_at,
           0 AS kind,NULL::timestamptz AS start_at,NULL::timestamptz AS end_at,
           coalesce(c.good_count,0)::numeric AS good,coalesce(c.total_count,0)::numeric AS total,
           CASE WHEN c.outcome='observed' THEN 1 ELSE 0 END::bigint AS observed,
           CASE WHEN c.outcome='complete_empty' THEN 1 ELSE 0 END::bigint AS known_empty,
           CASE WHEN c.outcome='observed' AND c.total_count=0 THEN 1 ELSE 0 END::bigint AS zero_traffic,
           CASE WHEN c.outcome='observed' THEN c.interval_start END AS first_observation,
           CASE WHEN c.outcome='observed' THEN c.interval_end END AS last_observation,c.recorded_at AS latest_receipt
    FROM anchors a CROSS JOIN measurement m
    -- Preserve a parameterized range scan for each anchor, not a year-wide join.
    CROSS JOIN LATERAL (
        SELECT c.interval_start,(c.interval_start+m.polling_interval_seconds*interval '1 second')::timestamptz AS interval_end,
               c.outcome,c.recorded_at,c.good_count,c.total_count
        FROM public.almanac_sli_observations c
        WHERE c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
          AND c.interval_start>=a.start_at AND c.interval_start<a.end_at
          AND c.interval_start+m.polling_interval_seconds*interval '1 second'<=a.end_at
        OFFSET 0
    ) c
    UNION ALL
    SELECT a.lane,a.start_at,a.end_at,
           CASE WHEN a.lane='a' THEN -extract(epoch FROM e.start_at) ELSE extract(epoch FROM e.end_at) END,
           1,e.start_at,e.end_at,0::numeric,0::numeric,0::bigint,0::bigint,0::bigint,NULL::timestamptz,NULL::timestamptz,NULL::timestamptz
    FROM anchors a JOIN edges e ON e.lane=a.lane AND e.anchor_at=a.anchor_at
), edge_prefix AS MATERIALIZED (
    SELECT lane,start_at,end_at,kind,
           sum(good) OVER w AS good,sum(total) OVER w AS total,
           sum(observed) OVER w AS observed,sum(known_empty) OVER w AS known_empty,sum(zero_traffic) OVER w AS zero_traffic,
           min(first_observation) OVER w AS first_observation,max(last_observation) OVER w AS last_observation,max(latest_receipt) OVER w AS latest_receipt
    FROM edge_input
    WINDOW w AS (PARTITION BY lane,anchor_start,anchor_end ORDER BY sort_at,kind ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
), edge_totals AS (
    SELECT * FROM edge_prefix WHERE kind<>0
)
SELECT r.ordinal,(t.good+coalesce(a.good,0)+coalesce(b.good,0))::numeric::text AS good,
       (t.total+coalesce(a.total,0)+coalesce(b.total,0))::numeric::text AS total,
       (t.observed+coalesce(a.observed,0)+coalesce(b.observed,0))::bigint AS observed,
       (t.known_empty+coalesce(a.known_empty,0)+coalesce(b.known_empty,0))::bigint AS known_empty,
       (t.zero_traffic+coalesce(a.zero_traffic,0)+coalesce(b.zero_traffic,0))::bigint AS zero_traffic,
       LEAST(t.first_observation,a.first_observation,b.first_observation)::timestamptz AS first_observation,
       GREATEST(t.last_observation,a.last_observation,b.last_observation)::timestamptz AS last_observation,
       GREATEST(t.latest_receipt,a.latest_receipt,b.latest_receipt)::timestamptz AS latest_receipt,
       coalesce(t.prepared,false)::boolean AS prepared,coalesce(t.exhausted,false)::boolean AS exhausted
FROM requested r LEFT JOIN totals t ON t.full_start=r.full_start AND t.full_end=r.full_end
LEFT JOIN edge_totals a ON a.start_at=r.edge_a_start AND a.end_at=r.edge_a_end
LEFT JOIN edge_totals b ON b.start_at=r.edge_b_start AND b.end_at=r.edge_b_end
ORDER BY r.ordinal;
