-- Fixture publication precondition, not a product currentness/latency gate.
-- One snapshot and exact IDs avoid urgency pagination changing under ingestion.
WITH scope AS (
    SELECT s.id,d.revision,d.measurement_revision,g.id AS generation_id,g.exclusion_revision,
           g.calculation_version,g.evaluated_through,g.published_at,p.payload,
           (p.payload #>> '{_budget_cache,closed_through}')::timestamptz AS closed_through,
           (p.payload #>> '{_budget_cache,range_start}')::timestamptz AS range_start,
           (p.payload #>> '{_budget_cache,published_at}')::timestamptz AS cache_published_at,
           greatest(d.evaluation_valid_from,m.effective_from,g.retention_start) AS valid_from,
           w.built_through,w.dirty_from,w.dirty_to
    FROM unnest($1) wanted(id)
    JOIN public.almanac_slos s ON s.id=wanted.id
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision
    JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id AND g.slo_id=s.id
      AND g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
      AND g.calculation_version='occurrences-operational-v1' AND g.exclusion_revision=0
    JOIN public.almanac_slo_extrema_cursor w ON w.slo_id=s.id AND w.definition_revision=d.revision
      AND w.measurement_revision=d.measurement_revision AND w.calculation_version=g.calculation_version
), coverage AS (
    SELECT scope.*,date_trunc('hour',greatest(range_start,valid_from) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS eligible_start
    FROM scope
)
SELECT count(*) FROM coverage c
WHERE c.payload->>'budget_history_state'='ready'
  AND c.payload #>> '{budget_history_generation,generation_id}'=c.generation_id::text
  AND c.payload #>> '{budget_history_generation,definition_revision}'=c.revision::text
  AND c.payload #>> '{budget_history_generation,measurement_revision}'=c.measurement_revision::text
  AND c.payload #>> '{budget_history_generation,calculation_version}'=c.calculation_version
  AND c.payload #>> '{budget_history_generation,exclusion_revision}'='0'
  AND jsonb_array_length(c.payload->'budget_sparkline') BETWEEN 1 AND 120
  AND c.closed_through>=$2
  AND c.closed_through=date_trunc('hour',c.evaluated_through AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
  AND c.range_start<c.evaluated_through AND c.range_start>=c.evaluated_through-interval '8 days'
  AND c.built_through>=c.closed_through
  AND NOT coalesce(c.dirty_from<c.closed_through AND c.dirty_to>c.eligible_start,false)
  AND (SELECT count(*) FROM public.almanac_slo_extrema e
       WHERE e.slo_id=c.id AND e.definition_revision=c.revision AND e.measurement_revision=c.measurement_revision
         AND e.calculation_version=c.calculation_version AND e.exclusion_revision=0 AND e.resolution_seconds=3600
         AND e.range_start>=c.eligible_start AND e.range_end<=c.closed_through)
      = greatest(0,extract(epoch FROM (c.closed_through-c.eligible_start))/3600)
  AND c.cache_published_at=(SELECT max(e.published_at) FROM public.almanac_slo_extrema e
       WHERE e.slo_id=c.id AND e.definition_revision=c.revision AND e.measurement_revision=c.measurement_revision
         AND e.calculation_version=c.calculation_version AND e.exclusion_revision=0 AND e.resolution_seconds=3600
         AND e.range_start>=date_trunc('hour',c.range_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
         AND e.range_end<=c.closed_through)
  AND c.cache_published_at<=c.published_at
