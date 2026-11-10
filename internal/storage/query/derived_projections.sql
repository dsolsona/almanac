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
WHERE (f.slo_id IS NULL OR ((f.attempts<5 OR f.failing_since IS NULL OR f.failing_since>sqlc.arg(now)::timestamptz-interval '7 days') AND f.next_attempt_at<=sqlc.arg(now)::timestamptz))
  AND (g.id IS NULL OR g.definition_revision<>d.revision
   OR g.measurement_revision<>d.measurement_revision
   OR g.calculation_version<>sqlc.arg(calculation_version)
   OR g.retention_start<>to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
   OR g.source_generation<>coalesce(source.generation,0)
   OR g.evaluated_through<to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
   OR EXISTS (SELECT 1 FROM public.almanac_slo_extrema e
      WHERE e.slo_id=s.id AND e.definition_revision=d.revision AND e.measurement_revision=d.measurement_revision
        AND e.calculation_version=sqlc.arg(calculation_version) AND e.exclusion_revision=0 AND e.resolution_seconds=3600
        AND e.range_start>=date_trunc('hour',(g.evaluated_through-interval '7 days') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        AND e.range_end<=date_trunc('hour',g.evaluated_through AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        AND e.published_at>coalesce((p.payload#>>'{_budget_cache,published_at}')::timestamptz,'-infinity'::timestamptz)))
ORDER BY f.next_attempt_at NULLS FIRST, g.evaluated_through NULLS FIRST, s.id LIMIT sqlc.arg(batch_limit);

-- Waiting on source work is schedulable state, not a failed calculation attempt.
-- Preserve an existing failure budget for this revision instead of resetting it.
-- name: DeferCurrentPreparation :exec
INSERT INTO public.almanac_slo_current_failures
    (slo_id,definition_revision,measurement_revision,calculation_version,attempts,next_attempt_at,last_error)
VALUES ($1,$2,$3,$4,0,$5::timestamptz+interval '2 seconds','current_waiting_for_source')
ON CONFLICT (slo_id) DO UPDATE
SET definition_revision=EXCLUDED.definition_revision,measurement_revision=EXCLUDED.measurement_revision,
    calculation_version=EXCLUDED.calculation_version,
    attempts=CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)
         = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN almanac_slo_current_failures.attempts ELSE 0 END,
    failing_since=CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)          = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN almanac_slo_current_failures.failing_since ELSE NULL END,
    next_attempt_at=EXCLUDED.next_attempt_at,last_error=EXCLUDED.last_error;

-- name: RecordCurrentPreparationFailure :exec
INSERT INTO public.almanac_slo_current_failures
    (slo_id,definition_revision,measurement_revision,calculation_version,attempts,next_attempt_at,last_error,failing_since)
VALUES ($1,$2,$3,$4,1,$5::timestamptz+interval '2 seconds',sqlc.arg(reason)::text,$5::timestamptz)
ON CONFLICT (slo_id) DO UPDATE
SET definition_revision=EXCLUDED.definition_revision,measurement_revision=EXCLUDED.measurement_revision,
    calculation_version=EXCLUDED.calculation_version,
    attempts=CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)
         = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN LEAST(almanac_slo_current_failures.attempts+1,5) ELSE 1 END,
    next_attempt_at=$5::timestamptz+CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)          = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN CASE WHEN LEAST(almanac_slo_current_failures.attempts+1,5)<5 THEN power(2,LEAST(almanac_slo_current_failures.attempts+1,5))*interval '1 second' WHEN coalesce(almanac_slo_current_failures.failing_since,$5::timestamptz)>$5::timestamptz-interval '24 hours' THEN interval '5 minutes' ELSE interval '1 hour' END
         ELSE interval '2 seconds' END,
    failing_since=CASE WHEN (almanac_slo_current_failures.definition_revision,almanac_slo_current_failures.measurement_revision,almanac_slo_current_failures.calculation_version)          = (EXCLUDED.definition_revision,EXCLUDED.measurement_revision,EXCLUDED.calculation_version)
         THEN coalesce(almanac_slo_current_failures.failing_since,$5::timestamptz) ELSE $5::timestamptz END,
    last_error=EXCLUDED.last_error;

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
-- The digest remains an archive/snapshot identity until F9-T05, not a read-time integrity check.
WITH value AS (
    SELECT sqlc.arg(payload)::jsonb || CASE WHEN sqlc.narg(budget_cache)::jsonb IS NULL THEN '{}'::jsonb
      ELSE jsonb_build_object('_budget_cache',sqlc.narg(budget_cache)::jsonb) END AS payload
)
INSERT INTO public.almanac_slo_current_projections
    (slo_id, generation_id, service_id, sort_name, urgency, payload,payload_digest)
SELECT sqlc.arg(slo_id),sqlc.arg(generation_id),sqlc.arg(service_id),sqlc.arg(sort_name),sqlc.arg(urgency),
       value.payload,sha256(convert_to(value.payload::text,'UTF8')) FROM value
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
         AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
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
