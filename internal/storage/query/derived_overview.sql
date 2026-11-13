-- name: CountSloOverviewGroups :one
WITH scope AS (
    SELECT p.urgency AS published_urgency,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL
             AND g.source_generation<=coalesce(source.generation,0)
             AND g.evaluated_through>=sqlc.arg(now)::timestamptz-interval '365 days',false)::boolean AS usable,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL
             AND g.source_generation=coalesce(source.generation,0)
             AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
             AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND coalesce(public.almanac_projection_deadline(p.current_valid_until),'-infinity'::timestamptz)>sqlc.arg(now)::timestamptz,false)::boolean AS current
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
    WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name||' '||s.display_name),lower(sqlc.narg(query_text)::text))>0)
      AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
-- Evaluate classification once per SLO, rather than once per count bucket.
), classified AS MATERIALIZED (
    SELECT CASE WHEN current OR (usable AND published_urgency='needs_attention') THEN published_urgency ELSE 'watch' END::text AS urgency
    FROM scope
)
SELECT count(*) FILTER(WHERE urgency='needs_attention')::bigint AS needs_attention,
       count(*) FILTER(WHERE urgency='watch')::bigint AS watch,
       count(*) FILTER(WHERE urgency='healthy')::bigint AS healthy
FROM classified;
