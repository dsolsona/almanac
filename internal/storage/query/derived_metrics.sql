-- name: ReadDerivedMetrics :one
WITH dirty AS (
 SELECT count(*) AS total,count(*) FILTER(WHERE dirty_range.attempts>=5 AND (dirty_range.lease_until IS NULL OR dirty_range.lease_until<=$1)) AS failed,
 coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-min(dirty_range.updated_at)))),0)::float8 AS age
 FROM public.almanac_derived_dirty_ranges dirty_range
), history AS (
 SELECT count(*) AS total,count(*) FILTER(WHERE j.state='failed') AS failed,
 coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-min(j.created_at) FILTER(WHERE j.state<>'failed')))),0)::float8 AS age
 FROM public.almanac_slo_history_jobs j
), current_state AS (
 SELECT coalesce(max(greatest(0,extract(epoch FROM ($1::timestamptz-g.evaluated_through)))),0)::float8 AS lag,
 count(*) FILTER(WHERE g.id IS NULL OR g.definition_revision<>d.revision OR g.measurement_revision<>d.measurement_revision
 OR g.calculation_version<>$2 OR g.source_generation<>coalesce(source.generation,0)
 OR g.evaluated_through<to_timestamp(floor(extract(epoch FROM $1::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
 OR g.retention_start<>to_timestamp(ceil(extract(epoch FROM ($1::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds)) AS pending
 FROM public.almanac_slos s
 JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
 LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
 LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
 LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
), canonical_size AS (
 SELECT coalesce(sum(pg_total_relation_size(relid)),0)::bigint AS bytes
 FROM (SELECT tree.relid FROM pg_partition_tree('public.almanac_sli_coverage') AS tree(relid,parentrelid,isleaf,level) WHERE tree.isleaf
 UNION ALL SELECT tree.relid FROM pg_partition_tree('public.almanac_sli_observation_values') AS tree(relid,parentrelid,isleaf,level) WHERE tree.isleaf) partitions
), derived_size AS (
 SELECT coalesce(sum(pg_total_relation_size(table_name::regclass)),0)::bigint AS bytes
 FROM unnest(ARRAY['public.almanac_sli_hourly_aggregates','public.almanac_derived_dirty_ranges',
 'public.almanac_sli_source_generations','public.almanac_slo_derived_generations','public.almanac_slo_current_projections',
 'public.almanac_slo_current_failures','public.almanac_slo_history_jobs','public.almanac_slo_history_blocks',
 'public.almanac_slo_display_blocks','public.almanac_derived_cache_usage',
 'public.almanac_slo_overview_snapshots','public.almanac_slo_overview_snapshot_items']) table_name
)
SELECT dirty.total AS dirty_keys,dirty.failed AS failed_keys,dirty.age AS oldest_dirty_seconds,
 history.total AS history_jobs,history.failed AS failed_history_jobs,history.age AS oldest_history_seconds,
 current_state.lag AS current_lag_seconds,current_state.pending AS pending_current,
 usage.payload_bytes AS cache_bytes,2147483648::bigint AS cache_limit_bytes,
 canonical_size.bytes AS canonical_bytes,derived_size.bytes AS derived_bytes
FROM dirty,history,current_state,public.almanac_derived_cache_usage usage,canonical_size,derived_size;
