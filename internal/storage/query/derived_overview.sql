-- name: OperationalScopeFingerprint :one
WITH scope AS (
    SELECT s.id,s.version,s.display_name,service.display_name AS service_name,measurement.display_name AS sli_name,
           d.revision,d.measurement_revision,p.generation_id,p.payload_digest,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version)
             AND g.source_generation<=coalesce(source.generation,0)
             AND g.evaluated_through>=sqlc.arg(now)::timestamptz-interval '365 days'
             AND EXISTS(SELECT 1 FROM public.almanac_slo_projection_payloads a WHERE a.digest=p.payload_digest),false) AS usable,
           coalesce(source.generation,0) AS source_generation,
           coalesce(f.attempts>=5,false) AS current_failed,
           EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges dirty WHERE dirty.sli_id=d.sli_id
             AND dirty.measurement_revision=d.measurement_revision AND dirty.attempts>=5
             AND (dirty.lease_until IS NULL OR dirty.lease_until<=sqlc.arg(now)::timestamptz)) AS source_failed,
           EXISTS(SELECT 1 FROM public.almanac_slo_extrema_cursor w WHERE w.slo_id=s.id AND w.definition_revision=d.revision
             AND w.measurement_revision=d.measurement_revision AND w.calculation_version=sqlc.arg(calculation_version)
             AND w.attempts>=5) AS budget_failed,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version)
             AND g.source_generation=coalesce(source.generation,0)
             AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
             AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND EXISTS(SELECT 1 FROM public.almanac_slo_projection_payloads a WHERE a.digest=p.payload_digest),false) AS current
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    JOIN public.almanac_services service ON service.id=d.service_id
    JOIN public.almanac_slis measurement ON measurement.id=d.sli_id
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
    LEFT JOIN public.almanac_slo_current_failures f ON f.slo_id=s.id AND f.definition_revision=d.revision
      AND f.measurement_revision=d.measurement_revision AND f.calculation_version=sqlc.arg(calculation_version)
    WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name||' '||s.display_name),lower(sqlc.narg(query_text)::text))>0)
      AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
    ORDER BY s.id LIMIT 10001
)
SELECT sha256(convert_to(coalesce(jsonb_agg(jsonb_build_array(id,version,display_name,service_name,sli_name,
           revision,measurement_revision,generation_id,source_generation,encode(payload_digest,'hex'),budget_failed,current,usable,current_failed,source_failed) ORDER BY id),'[]'::jsonb)::text,'UTF8'))::bytea AS fingerprint,
       count(*)::bigint AS definition_count,coalesce(bool_and(current),true)::boolean AS current
FROM scope;

-- name: FindReusableSloOverviewSnapshot :one
SELECT id FROM public.almanac_slo_overview_snapshots
WHERE actor_id=sqlc.arg(actor_id) AND session_verifier=sqlc.arg(session_verifier) AND role=sqlc.arg(role)
  AND query_text=sqlc.arg(query_text) AND service_id IS NOT DISTINCT FROM sqlc.narg(service_id)::uuid
  AND urgency IS NOT DISTINCT FROM sqlc.narg(urgency)::text AND page_limit=sqlc.arg(page_limit)
  AND state='ready' AND expires_at>sqlc.arg(now) AND publication_fingerprint=sqlc.arg(fingerprint)::bytea
ORDER BY created_at DESC,id LIMIT 1;

-- name: SetSloOverviewFingerprint :exec
UPDATE public.almanac_slo_overview_snapshots SET publication_fingerprint=$2 WHERE id=$1 AND state='ready';
