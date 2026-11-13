-- Experiment only. Same first ten parameters as ReadRetainedWindowBatch;
-- $11 bounds distinct dirty hours, $12 bounds raw interval visits.
WITH measurement AS MATERIALIZED (
 SELECT d.polling_interval_seconds AS cadence
 FROM public.almanac_sli_measurements m JOIN public.almanac_datasources d ON d.id=m.datasource_id
 WHERE m.sli_id=$1 AND m.revision=$2
), requested AS MATERIALIZED (
 SELECT generate_series(1,cardinality($3::timestamptz[]))::integer AS ordinal,
 unnest($3::timestamptz[]) AS window_end,
 unnest($4::timestamptz[]) AS full_start,unnest($5::timestamptz[]) AS full_end,
 unnest($6::timestamptz[]) AS a_start,unnest($7::timestamptz[]) AS a_end,
 unnest($8::timestamptz[]) AS b_start,unnest($9::timestamptz[]) AS b_end
), dirty AS MATERIALIZED (
 SELECT d.range_start,d.attempts,d.lease_until
 FROM public.almanac_derived_dirty_ranges d
 WHERE d.sli_id=$1 AND d.measurement_revision=$2 AND EXISTS (
 SELECT 1 FROM requested r WHERE r.full_start<r.full_end AND d.range_start<r.full_end AND d.range_end>r.full_start)
 ORDER BY d.range_start LIMIT ($11::integer+1)
), ranges AS MATERIALIZED (
 SELECT ordinal,a_start AS start_at,a_end AS end_at,a_end AS complete_through FROM requested WHERE a_start<a_end
 UNION ALL
 SELECT ordinal,b_start,b_end,b_end FROM requested WHERE b_start<b_end
 UNION ALL
 SELECT r.ordinal,d.range_start,d.range_start+interval '1 hour',r.full_end
 FROM requested r JOIN dirty d ON d.range_start>=r.full_start AND d.range_start<r.full_end
), budget AS MATERIALIZED (
 SELECT count(*)<=$11::integer AND cardinality($3::timestamptz[]) BETWEEN 1 AND 4
 AND coalesce((SELECT sum(ceil(extract(epoch FROM (g.end_at-g.start_at))/m.cadence)+1)
 FROM ranges g CROSS JOIN measurement m),0)<=$12::integer AS allowed,
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d
 WHERE d.sli_id=$1 AND d.measurement_revision=$2 AND d.attempts>=5
 AND (d.lease_until IS NULL OR d.lease_until<=$10::timestamptz)
 AND EXISTS(SELECT 1 FROM requested r WHERE r.full_start<r.full_end
 AND d.range_start<r.full_end AND d.range_end>r.full_start)) AS exhausted,
 count(*)::bigint AS dirty_hours
 FROM dirty
), raw AS MATERIALIZED (
 SELECT g.ordinal,c.* FROM budget b CROSS JOIN ranges g CROSS JOIN measurement m
 CROSS JOIN LATERAL (
 SELECT o.interval_start,o.interval_start+m.cadence*interval '1 second' AS interval_end,
 o.outcome,o.good_count,o.total_count,o.recorded_at
 FROM public.almanac_sli_observations o
 WHERE b.allowed AND NOT b.exhausted AND o.sli_id=$1 AND o.measurement_revision=$2
 AND o.interval_start>=g.start_at AND o.interval_start<g.end_at
 AND o.interval_start+m.cadence*interval '1 second'<=g.complete_through
 OFFSET 0
 ) c
 LIMIT ($12::integer+1)
), readiness AS MATERIALIZED (
 SELECT b.*, (SELECT count(*) FROM raw)::bigint AS raw_rows,
 b.allowed AND NOT b.exhausted AND (SELECT count(*) FROM raw)<=$12::integer AS prepared
 FROM budget b
), contributions AS (
 SELECT g.ordinal,
 coalesce(sum(g.good_count) FILTER(WHERE g.outcome='observed'),0)::numeric AS good,
 coalesce(sum(g.total_count) FILTER(WHERE g.outcome='observed'),0)::numeric AS total,
 count(*) FILTER(WHERE g.outcome='observed') AS observed,
 count(*) FILTER(WHERE g.outcome='complete_empty') AS known_empty,
 count(*) FILTER(WHERE g.outcome='observed' AND g.total_count=0) AS zero_traffic,
 min(g.interval_start) FILTER(WHERE g.outcome='observed') AS first_observation,
 max(g.interval_end) FILTER(WHERE g.outcome='observed') AS last_observation,
 max(g.recorded_at) AS latest_receipt
 FROM raw g CROSS JOIN readiness b WHERE b.prepared GROUP BY g.ordinal
 UNION ALL
 SELECT r.ordinal,coalesce(sum(h.good),0),coalesce(sum(h.total),0),
 coalesce(sum(h.observed_intervals),0)::bigint,coalesce(sum(h.known_empty_intervals),0)::bigint,
 coalesce(sum(h.zero_traffic_intervals),0)::bigint,min(h.first_observation_at),max(h.last_observation_at),max(h.latest_receipt_at)
 FROM requested r CROSS JOIN readiness b
 CROSS JOIN LATERAL (
 SELECT h.* FROM public.almanac_sli_hourly_aggregates h
 WHERE b.prepared AND h.sli_id=$1 AND h.measurement_revision=$2
 AND h.hour_start>=r.full_start AND h.hour_start<r.full_end
 AND NOT EXISTS(SELECT 1 FROM dirty d WHERE d.range_start=h.hour_start)
 OFFSET 0
 ) h GROUP BY r.ordinal
)
SELECT r.ordinal,
 CASE WHEN b.prepared THEN coalesce(sum(c.good),0)::numeric::text END AS good,
 CASE WHEN b.prepared THEN coalesce(sum(c.total),0)::numeric::text END AS total,
 coalesce(sum(c.observed),0)::bigint AS observed,coalesce(sum(c.known_empty),0)::bigint AS known_empty,
 coalesce(sum(c.zero_traffic),0)::bigint AS zero_traffic,min(c.first_observation) AS first_observation,
 max(c.last_observation) AS last_observation,max(c.latest_receipt) AS latest_receipt,
 b.prepared,b.exhausted,b.dirty_hours,b.raw_rows
FROM requested r CROSS JOIN readiness b LEFT JOIN contributions c ON c.ordinal=r.ordinal
GROUP BY r.ordinal,b.prepared,b.exhausted,b.dirty_hours,b.raw_rows ORDER BY r.ordinal;
