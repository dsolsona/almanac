-- name: ReadMaintenanceEvaluationIdentity :one
SELECT d.sli_id,s.definition_revision,d.measurement_revision,d.cadence_seconds,
 m.measurement_kind,m.component_mode,coalesce(g.generation,0)::bigint AS source_generation,
 e.revision AS exclusion_revision,current_setting('transaction_isolation')::text AS isolation
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision
JOIN public.almanac_slo_exclusion_state e ON e.slo_id=s.id
LEFT JOIN public.almanac_sli_source_generations g ON g.sli_id=d.sli_id AND g.measurement_revision=d.measurement_revision
WHERE s.id=$1;

-- name: ReadMaintenanceEvaluationBuild :one
SELECT * FROM public.almanac_maintenance_builds WHERE slo_id=$1 AND target_revision=$2;
-- name: ReadMaintenanceEvaluationCandidate :one
SELECT * FROM public.almanac_maintenance_builds
WHERE slo_id=$1 AND target_revision<=$2 AND phase='complete'
ORDER BY target_revision DESC LIMIT 1;

-- name: ReadMaintenanceEvaluationHours :many
SELECT x.hour_start::timestamptz AS hour_start,coalesce(h.source_generation,0)::bigint AS source_generation,
 coalesce(h.good,0)::numeric::text AS good,coalesce(h.total,0)::numeric::text AS total,
 coalesce(h.observed_intervals,0)::bigint AS observed,coalesce(h.known_empty_intervals,0)::bigint AS known_empty,
 coalesce(h.zero_traffic_intervals,0)::bigint AS zero_traffic,h.first_observation_at,h.last_observation_at,h.latest_receipt_at,
 coalesce((h.observed_intervals+h.known_empty_intervals>0 AND
  (h.receipt_source_generation IS DISTINCT FROM h.source_generation OR h.certified_receipt_at IS DISTINCT FROM h.latest_receipt_at OR h.latest_receipt_at IS NULL)),false)::boolean AS receipt_unknown,
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision) AND d.range_start<x.hour_start+interval '1 hour' AND d.range_end>x.hour_start) AS pending,
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision) AND d.range_start<x.hour_start+interval '1 hour' AND d.range_end>x.hour_start AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)) AS exhausted,
 CASE WHEN h.sli_id IS NULL THEN EXISTS(SELECT 1 FROM public.almanac_sli_observations o WHERE o.sli_id=sqlc.arg(sli_id) AND o.measurement_revision=sqlc.arg(measurement_revision) AND o.interval_start>=x.hour_start AND o.interval_start<x.hour_start+interval '1 hour') ELSE false END AS unprepared_raw
FROM unnest(sqlc.arg(hours)::timestamptz[]) x(hour_start)
LEFT JOIN public.almanac_sli_hourly_aggregates h ON h.sli_id=sqlc.arg(sli_id) AND h.measurement_revision=sqlc.arg(measurement_revision) AND h.hour_start=x.hour_start
ORDER BY x.hour_start;

-- name: ReadMaintenanceEvaluationEdges :many
-- The caller admits the sum of per-range bounds before this query. LIMIT is
-- inside each indexed range, before inspecting outcome or numeric fields.
SELECT r.ordinal::integer AS ordinal,o.interval_start,o.outcome,
 coalesce(o.good_count,0)::numeric::text AS good,coalesce(o.total_count,0)::numeric::text AS total,
 o.recorded_at,o.ingestion_generation
FROM (SELECT unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,unnest(sqlc.arg(ends)::timestamptz[]) AS end_at,unnest(sqlc.arg(caps)::integer[]) AS row_cap,generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[])) AS ordinal) r
CROSS JOIN LATERAL (
 SELECT * FROM public.almanac_sli_observations o
 WHERE o.sli_id=sqlc.arg(sli_id) AND o.measurement_revision=sqlc.arg(measurement_revision) AND o.interval_start>=r.start_at AND o.interval_start<r.end_at
 ORDER BY o.interval_start LIMIT r.row_cap
) o ORDER BY r.ordinal,o.interval_start;

-- name: ReadMaintenanceEvaluationFragments :many
-- One predecessor and bounded forward starts per admitted edge; no full mask.
SELECT r.ordinal::integer AS ordinal,c.starts_at,c.ends_at
FROM (SELECT unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,unnest(sqlc.arg(ends)::timestamptz[]) AS end_at,unnest(sqlc.arg(caps)::integer[]) AS row_cap,generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[])) AS ordinal) r
CROSS JOIN LATERAL (
 (SELECT f.starts_at,f.ends_at FROM public.almanac_maintenance_coverage f WHERE f.build_id=sqlc.arg(build_id) AND f.starts_at<r.start_at ORDER BY f.starts_at DESC LIMIT 1)
 UNION ALL
 (SELECT f.starts_at,f.ends_at FROM public.almanac_maintenance_coverage f WHERE f.build_id=sqlc.arg(build_id) AND f.starts_at>=r.start_at AND f.starts_at<r.end_at ORDER BY f.starts_at LIMIT r.row_cap)
) c
WHERE c.ends_at>r.start_at ORDER BY r.ordinal,c.starts_at;

-- name: ReadMaintenanceEvaluationScopedHours :many
-- Key arrays are admitted before SQL. Prefer exact build; only when absent seek
-- one older exact-source candidate. Its proof is validated by the same RR reader.
SELECT r.ordinal::integer AS ordinal,h.slo_id,h.definition_revision,h.measurement_revision,h.build_id,h.hour_start,h.source_generation,h.sli_id,h.exclusion_revision,h.prepared_at,
 h.eligible_good::text AS eligible_good,h.eligible_total::text AS eligible_total,h.eligible_expected,h.eligible_observed,h.eligible_known_empty,h.eligible_zero_traffic,h.eligible_first_observation,h.eligible_last_observation,h.eligible_latest_receipt,
 h.excluded_good::text AS excluded_good,h.excluded_total::text AS excluded_total,h.excluded_expected,h.excluded_observed,h.excluded_known_empty,h.excluded_zero_traffic,h.excluded_first_observation,h.excluded_last_observation,h.excluded_latest_receipt
FROM (SELECT unnest(sqlc.arg(hours)::timestamptz[]) AS hour_start,unnest(sqlc.arg(generations)::bigint[]) AS source_generation,generate_series(1,cardinality(sqlc.arg(hours)::timestamptz[])) AS ordinal) r
CROSS JOIN LATERAL (
 SELECT * FROM public.almanac_slo_maintenance_hours h
 WHERE h.slo_id=sqlc.arg(slo_id) AND h.definition_revision=sqlc.arg(definition_revision) AND h.measurement_revision=sqlc.arg(measurement_revision) AND h.hour_start=r.hour_start AND h.source_generation=r.source_generation AND h.build_id=sqlc.arg(build_id)
 UNION ALL
 (SELECT * FROM public.almanac_slo_maintenance_hours h
 WHERE h.slo_id=sqlc.arg(slo_id) AND h.definition_revision=sqlc.arg(definition_revision) AND h.measurement_revision=sqlc.arg(measurement_revision) AND h.hour_start=r.hour_start AND h.source_generation=r.source_generation AND h.exclusion_revision<sqlc.arg(exclusion_revision)
 AND NOT EXISTS(SELECT 1 FROM public.almanac_slo_maintenance_hours exact WHERE exact.slo_id=sqlc.arg(slo_id) AND exact.definition_revision=sqlc.arg(definition_revision) AND exact.measurement_revision=sqlc.arg(measurement_revision) AND exact.hour_start=r.hour_start AND exact.source_generation=r.source_generation AND exact.build_id=sqlc.arg(build_id))
 ORDER BY h.exclusion_revision DESC LIMIT 1)
) h ORDER BY r.ordinal;

-- name: ReadMaintenanceEvaluationProofs :many
-- Caller validates one compatibility authority in this same RR snapshot. Every
-- candidate is one bounded GiST overlap/PK proof, not an event-history replay.
WITH requested AS (
 SELECT generate_series(1,cardinality(sqlc.arg(computed)::bigint[]))::integer AS ordinal,
 unnest(sqlc.arg(computed)::bigint[]) AS computed,unnest(sqlc.arg(requested)::bigint[]) AS requested,
 unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,unnest(sqlc.arg(ends)::timestamptz[]) AS end_at
)
SELECT r.ordinal,
 EXISTS(SELECT 1 FROM public.almanac_maintenance_changed_inputs c
 WHERE c.slo_id=sqlc.arg(slo_scope_id)::uuid AND c.scope_key @> sqlc.arg(scope_ordinal)::bigint
 AND c.revision_key && int8range(r.computed+1,CASE WHEN r.requested=9223372036854775807 THEN NULL ELSE r.requested+1 END,'[)')
 AND c.input_range && tstzrange(r.start_at,r.end_at,'[)') LIMIT 1)::boolean AS changed,
 (SELECT c.id FROM public.almanac_maintenance_comparisons c
 JOIN public.almanac_maintenance_builds a ON a.id=c.first_build
 JOIN public.almanac_maintenance_builds b ON b.id=c.second_build
 WHERE a.slo_id=sqlc.arg(slo_scope_id)::uuid AND b.slo_id=sqlc.arg(slo_scope_id)::uuid
 AND a.target_revision=r.computed AND b.target_revision=r.requested AND c.state='equal')::uuid AS comparison_id
FROM requested r ORDER BY r.ordinal;

-- name: ReadMaintenanceEvaluationComparisons :many
SELECT r.ordinal::integer AS ordinal,CASE WHEN c.state IN ('equal','different','cancelled') THEN c.state WHEN j.state IN ('unavailable','exhausted') THEN j.state ELSE coalesce(c.state,'absent') END::text AS state
FROM (SELECT unnest(sqlc.arg(first_builds)::uuid[]) AS first_build,unnest(sqlc.arg(second_builds)::uuid[]) AS second_build,generate_series(1,cardinality(sqlc.arg(first_builds)::uuid[])) AS ordinal) r
LEFT JOIN public.almanac_maintenance_comparisons c ON c.first_build=r.first_build AND c.second_build=r.second_build
LEFT JOIN public.almanac_maintenance_preparation j ON j.kind='comparison' AND j.slo_id=sqlc.arg(slo_id) AND j.target_revision=sqlc.arg(target_revision)
 AND j.build_id=r.first_build AND j.second_build=r.second_build AND j.sli_id IS NULL AND j.definition_revision IS NULL AND j.measurement_revision IS NULL AND j.hour_start IS NULL AND j.source_generation IS NULL
ORDER BY r.ordinal;

-- name: ReadMaintenanceEvaluationBuildHealth :one
-- Exact immutable mask/index key; never infer health from another revision.
SELECT coalesce((SELECT CASE WHEN state='exhausted' THEN 'maintenance_preparation_failed' WHEN state='unavailable' THEN 'maintenance_history_unavailable' ELSE '' END
 FROM public.almanac_maintenance_preparation
 WHERE kind=sqlc.arg(kind) AND slo_id=sqlc.arg(slo_id) AND target_revision=sqlc.arg(target_revision) AND build_id=sqlc.arg(build_id)
 AND second_build IS NULL AND sli_id IS NULL AND definition_revision IS NULL AND measurement_revision IS NULL AND hour_start IS NULL AND source_generation IS NULL),'')::text AS reason;

-- name: ReadMaintenanceEvaluationHourHealth :many
-- Admitted physical-hour keys use the complete natural-key index, including G.
SELECT r.ordinal::integer AS ordinal,coalesce(CASE WHEN j.state='exhausted' THEN 'maintenance_preparation_failed' WHEN j.state='unavailable' THEN 'maintenance_history_unavailable' ELSE '' END,'')::text AS reason
FROM (SELECT unnest(sqlc.arg(hours)::timestamptz[]) AS hour_start,unnest(sqlc.arg(generations)::bigint[]) AS source_generation,generate_series(1,cardinality(sqlc.arg(hours)::timestamptz[])) AS ordinal) r
LEFT JOIN public.almanac_maintenance_preparation j ON j.kind='scoped_hour' AND j.slo_id=sqlc.arg(slo_id) AND j.target_revision=sqlc.arg(target_revision) AND j.build_id=sqlc.arg(build_id) AND j.second_build IS NULL
 AND j.sli_id=sqlc.arg(sli_id) AND j.definition_revision=sqlc.arg(definition_revision) AND j.measurement_revision=sqlc.arg(measurement_revision) AND j.hour_start=r.hour_start AND j.source_generation=r.source_generation
ORDER BY r.ordinal;
