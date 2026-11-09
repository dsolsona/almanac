-- The measurement lock serializes acceptance. All three writes share that
-- transaction, after replay/conflict checks; a failed insert rolls this back.
-- name: AdvanceSliSourceGeneration :one
INSERT INTO public.almanac_sli_source_generations(sli_id,measurement_revision,generation,updated_at)
VALUES(sqlc.arg(sli_id),sqlc.arg(measurement_revision),1,sqlc.arg(recorded_at))
ON CONFLICT(sli_id,measurement_revision) DO UPDATE
SET generation=almanac_sli_source_generations.generation+1,
    updated_at=GREATEST(almanac_sli_source_generations.updated_at,EXCLUDED.updated_at)
RETURNING generation;

-- name: InsertSliObservation :exec
INSERT INTO public.almanac_sli_observations
(sli_id,measurement_revision,interval_start,outcome,good_count,total_count,result_digest,
 error_code,missing_components,execution_id,delivery_id,agent_id,session_generation,
 configuration_revision,lease_id,agent_created_at,recorded_at,ingestion_generation)
VALUES(sqlc.arg(sli_id),sqlc.arg(measurement_revision),sqlc.arg(interval_start),sqlc.arg(outcome),
 sqlc.narg(good_count)::text::numeric(38,12),sqlc.narg(total_count)::text::numeric(38,12),sqlc.arg(result_digest),
 sqlc.narg(error_code),sqlc.arg(missing_components),sqlc.arg(execution_id),sqlc.arg(delivery_id),
 sqlc.arg(agent_id),sqlc.arg(session_generation),sqlc.arg(configuration_revision),sqlc.arg(lease_id),
 sqlc.arg(agent_created_at),sqlc.arg(recorded_at),sqlc.arg(ingestion_generation));

-- name: MarkSliObservation :exec
WITH dirty AS (
    INSERT INTO public.almanac_derived_dirty_ranges
    (sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
    VALUES(sqlc.arg(sli_id),sqlc.arg(measurement_revision),sqlc.arg(generation),
      date_trunc('hour',sqlc.arg(interval_start)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
      GREATEST((date_trunc('hour',sqlc.arg(interval_start)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour',sqlc.arg(interval_end)::timestamptz),
      sqlc.arg(recorded_at),sqlc.arg(recorded_at))
    ON CONFLICT(sli_id,measurement_revision,range_start) DO UPDATE
    SET generation=EXCLUDED.generation,
        range_start=LEAST(almanac_derived_dirty_ranges.range_start,EXCLUDED.range_start),
        range_end=GREATEST(almanac_derived_dirty_ranges.range_end,EXCLUDED.range_end),
        next_attempt_at=LEAST(almanac_derived_dirty_ranges.next_attempt_at,EXCLUDED.next_attempt_at),
        updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at,EXCLUDED.updated_at),
        attempts=0,failing_since=NULL
)
UPDATE public.almanac_slo_extrema_cursor w
SET dirty_from=LEAST(w.dirty_from,date_trunc('hour',sqlc.arg(interval_start)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
    dirty_to=GREATEST(w.dirty_to,(date_trunc('hour',sqlc.arg(interval_start)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
        +(GREATEST(d.rolling_window_seconds,21600)+3600)*interval '1 second'),
    version=w.version+1,attempts=0,failing_since=NULL,last_error=NULL,
    next_attempt_at=LEAST(w.next_attempt_at,sqlc.arg(recorded_at)::timestamptz)
FROM public.almanac_slo_definitions d
JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
  AND w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;
