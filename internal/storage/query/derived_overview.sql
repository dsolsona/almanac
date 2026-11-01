-- name: OperationalScopeFingerprint :one
WITH scope AS (
    SELECT s.id,s.version,s.display_name,service.display_name AS service_name,measurement.display_name AS sli_name,
           d.revision,d.measurement_revision,p.generation_id,p.payload_digest,
           coalesce(source.generation,0) AS source_generation,
           EXISTS(SELECT 1 FROM public.almanac_slo_history_jobs j WHERE j.slo_id=s.id AND j.definition_revision=d.revision
             AND j.measurement_revision=d.measurement_revision AND j.calculation_version=sqlc.arg(calculation_version)
             AND j.kind='overview' AND j.state='failed') AS budget_failed,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version)
             AND g.source_generation=coalesce(source.generation,0)
             AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds)
             AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND p.payload_digest=sha256(convert_to(p.payload::text,'UTF8')),false) AS current
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    JOIN public.almanac_services service ON service.id=d.service_id
    JOIN public.almanac_slis measurement ON measurement.id=d.sli_id
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
    WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name||' '||s.display_name),lower(sqlc.narg(query_text)::text))>0)
      AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
    ORDER BY s.id LIMIT 10001
)
SELECT sha256(convert_to(coalesce(jsonb_agg(jsonb_build_array(id,version,display_name,service_name,sli_name,
           revision,measurement_revision,generation_id,source_generation,encode(payload_digest,'hex'),budget_failed) ORDER BY id),'[]'::jsonb)::text,'UTF8'))::bytea AS fingerprint,
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

-- name: BudgetPreparationExhausted :one
SELECT EXISTS(SELECT 1 FROM public.almanac_slo_history_jobs j
WHERE j.slo_id=$1 AND j.definition_revision=$2 AND j.measurement_revision=$3
  AND j.calculation_version=$4 AND j.kind='overview' AND j.state='failed');

-- name: QueueVisibleBudgetHistory :execrows
WITH wanted AS (
    SELECT s.id, d.revision, d.measurement_revision, coalesce(source.generation,0) AS source_generation,
           to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds) AS at,
           to_timestamp(floor((floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds-604800)/d.cadence_seconds)*d.cadence_seconds) AS start
           ,to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds) AS retention_start
    FROM unnest(sqlc.arg(slo_ids)::uuid[]) requested(id)
    JOIN public.almanac_slos s ON s.id=requested.id
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
)
INSERT INTO public.almanac_slo_history_jobs
    (slo_id,definition_revision,measurement_revision,source_generation,evaluated_through,range_start,range_end,burn_window_seconds,state,next_attempt_at,created_at,kind,calculation_version,retention_start)
SELECT w.id,w.revision,w.measurement_revision,w.source_generation,w.at,w.start,w.at,3600,'pending',sqlc.arg(now)::timestamptz,sqlc.arg(now)::timestamptz,'overview',sqlc.arg(calculation_version),w.retention_start
FROM wanted w
WHERE NOT EXISTS (
    SELECT 1 FROM public.almanac_slo_history_blocks b JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
    WHERE g.slo_id=w.id AND g.definition_revision=w.revision AND g.measurement_revision=w.measurement_revision
      AND g.source_generation=w.source_generation AND g.evaluated_through=w.at
      AND g.calculation_version=sqlc.arg(calculation_version) AND g.retention_start=w.retention_start
      AND b.range_start=w.start AND b.range_end=w.at AND b.burn_window_seconds=3600 AND b.budget_sparkline IS NOT NULL
)
AND NOT EXISTS (
    SELECT 1 FROM public.almanac_slo_history_jobs failed WHERE failed.slo_id=w.id AND failed.definition_revision=w.revision
    AND failed.measurement_revision=w.measurement_revision AND failed.kind='overview' AND failed.state='failed'
    AND failed.calculation_version=sqlc.arg(calculation_version)
)
AND (SELECT count(*) FROM public.almanac_slo_history_jobs queued WHERE queued.slo_id=w.id)<8
ORDER BY w.id
LIMIT greatest(0,64-(SELECT count(*) FROM public.almanac_slo_history_jobs))
ON CONFLICT (slo_id,definition_revision,measurement_revision,source_generation,evaluated_through,range_start,range_end,burn_window_seconds,kind,calculation_version,retention_start) DO NOTHING;
