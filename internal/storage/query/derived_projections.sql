-- name: ListDueDerivedObjectives :many
SELECT s.id FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
LEFT JOIN public.almanac_sli_source_generations source
    ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
LEFT JOIN public.almanac_slo_current_failures f
    ON f.slo_id=s.id AND f.definition_revision=d.revision
    AND f.measurement_revision=d.measurement_revision AND f.calculation_version=sqlc.arg(calculation_version)
WHERE (f.slo_id IS NULL OR (f.attempts<5 AND f.next_attempt_at<=sqlc.arg(now)::timestamptz))
  AND (g.id IS NULL OR g.definition_revision<>d.revision
   OR g.calculation_version<>sqlc.arg(calculation_version)
   OR g.retention_start<>to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds)
   OR p.payload_digest IS DISTINCT FROM sha256(convert_to(p.payload::text,'UTF8'))
   OR g.source_generation<>coalesce(source.generation,0)
   OR g.evaluated_through<to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds))
ORDER BY g.evaluated_through NULLS FIRST, s.id LIMIT sqlc.arg(batch_limit);

-- name: RecordCurrentPreparationFailure :exec
INSERT INTO public.almanac_slo_current_failures
    (slo_id,definition_revision,measurement_revision,calculation_version,attempts,next_attempt_at,last_error)
VALUES ($1,$2,$3,$4,1,$5::timestamptz+interval '2 seconds','current_preparation_failed')
ON CONFLICT (slo_id) DO UPDATE
SET definition_revision=EXCLUDED.definition_revision,measurement_revision=EXCLUDED.measurement_revision,
    calculation_version=EXCLUDED.calculation_version,
    attempts=CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)
         = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN LEAST(almanac_slo_current_failures.attempts+1,5) ELSE 1 END,
    next_attempt_at=$5::timestamptz+interval '32 seconds',last_error=EXCLUDED.last_error;

-- name: ClearCurrentPreparationFailure :exec
DELETE FROM public.almanac_slo_current_failures WHERE slo_id=$1;

-- name: CurrentPreparationExhausted :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_slo_current_failures f
    JOIN public.almanac_slos s ON s.id=f.slo_id AND s.definition_revision=f.definition_revision
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    WHERE f.slo_id=$1 AND f.measurement_revision=d.measurement_revision
      AND f.calculation_version=$2 AND f.attempts>=5
);

-- name: InsertDerivedGeneration :one
INSERT INTO public.almanac_slo_derived_generations
    (id, slo_id, definition_revision, measurement_revision, source_generation,
     calculation_version, evaluated_through, retention_start, published_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: PublishDerivedCurrentProjection :execrows
INSERT INTO public.almanac_slo_current_projections
    (slo_id, generation_id, service_id, sort_name, urgency, payload,payload_digest)
VALUES ($1,$2,$3,$4,$5,$6,sha256(convert_to($6::jsonb::text,'UTF8')))
ON CONFLICT (slo_id) DO UPDATE
SET generation_id=EXCLUDED.generation_id, service_id=EXCLUDED.service_id,
    sort_name=EXCLUDED.sort_name, urgency=EXCLUDED.urgency, payload=EXCLUDED.payload,payload_digest=EXCLUDED.payload_digest
WHERE EXISTS (
    SELECT 1 FROM public.almanac_slo_derived_generations old_generation,
                  public.almanac_slo_derived_generations new_generation
    WHERE old_generation.id=almanac_slo_current_projections.generation_id
      AND new_generation.id=EXCLUDED.generation_id
      AND (old_generation.definition_revision, old_generation.source_generation, old_generation.evaluated_through)
       <= (new_generation.definition_revision, new_generation.source_generation, new_generation.evaluated_through)
);

-- name: GetPublishedCurrentProjection :one
SELECT ((p.payload-'budget_sparkline'-'budget_history_generation'-'budget_history_state') ||
       jsonb_build_object('slo',(p.payload->'slo')||jsonb_build_object(
           'display_name',s.display_name,'version',s.version::text,'updated_at',s.updated_at,
           'service_display_name',service.display_name,'sli_display_name',measurement.display_name)))::jsonb AS payload,
       g.id AS generation_id, g.source_generation,
       g.definition_revision, g.measurement_revision, g.exclusion_revision,
       g.calculation_version, g.evaluated_through, g.published_at,g.retention_start,
       coalesce((g.definition_revision=s.definition_revision
         AND g.measurement_revision=d.measurement_revision
         AND g.calculation_version=sqlc.arg(calculation_version)
         AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds)
         AND p.payload_digest=sha256(convert_to(p.payload::text,'UTF8'))
         AND g.source_generation=coalesce(source.generation,0)
         AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)),false)::boolean AS current
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_services service ON service.id=d.service_id
JOIN public.almanac_slis measurement ON measurement.id=d.sli_id
LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
LEFT JOIN public.almanac_sli_source_generations source
    ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
WHERE s.id=sqlc.arg(slo_id);

-- name: ListPublishedOperationalProjections :many
SELECT s.id,(p.payload||CASE WHEN EXISTS(SELECT 1 FROM public.almanac_slo_history_jobs j
           WHERE j.slo_id=s.id AND j.definition_revision=d.revision AND j.measurement_revision=d.measurement_revision
             AND j.calculation_version=sqlc.arg(calculation_version) AND j.kind='overview' AND j.state='failed')
           THEN jsonb_build_object('budget_history_state','unavailable') ELSE '{}'::jsonb END
           ||jsonb_build_object('slo',(p.payload->'slo')||jsonb_build_object(
           'display_name',s.display_name,'version',s.version::text,'updated_at',s.updated_at,
           'service_display_name',service.display_name,'sli_display_name',measurement.display_name)))::jsonb AS payload,
       g.id AS generation_id, g.source_generation,
       g.evaluated_through, g.published_at,
       coalesce((g.definition_revision=s.definition_revision
         AND g.measurement_revision=d.measurement_revision
         AND g.calculation_version=sqlc.arg(calculation_version)
         AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '400 days'))/d.cadence_seconds)*d.cadence_seconds)
         AND p.payload_digest=sha256(convert_to(p.payload::text,'UTF8'))
         AND g.source_generation=coalesce(source.generation,0)
         AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)),false)::boolean AS current
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_services service ON service.id=d.service_id
JOIN public.almanac_slis measurement ON measurement.id=d.sli_id
LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
LEFT JOIN public.almanac_sli_source_generations source
    ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name || ' ' || s.display_name),lower(sqlc.narg(query_text)::text))>0)
  AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
ORDER BY lower(s.name), s.id LIMIT sqlc.arg(row_limit);
