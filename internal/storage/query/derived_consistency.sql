-- name: RestartStoppedDerivedWork :one
-- ADR-0034: work that stopped after seven days of continuous failure starts again once its SLI has
-- received observations since the failures began. Dirty hours restart in the ingestion trigger.
WITH changed AS (
    SELECT d.slo_id,d.revision AS definition_revision,d.measurement_revision,source.updated_at
    FROM public.almanac_slo_definitions d
    JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
    JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
), current_work AS (
    UPDATE public.almanac_slo_current_failures f SET attempts=0,failing_since=NULL,next_attempt_at=sqlc.arg(now)::timestamptz
    FROM changed c WHERE f.slo_id=c.slo_id AND f.definition_revision=c.definition_revision AND f.measurement_revision=c.measurement_revision
      AND f.attempts>=5 AND f.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' AND c.updated_at>f.failing_since
    RETURNING 1
), repair_work AS (
    UPDATE public.almanac_slo_series_repair w SET attempts=0,failing_since=NULL,next_attempt_at=sqlc.arg(now)::timestamptz
    FROM changed c WHERE w.slo_id=c.slo_id AND w.definition_revision=c.definition_revision AND w.measurement_revision=c.measurement_revision
      AND w.attempts>=5 AND w.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' AND c.updated_at>w.failing_since
    RETURNING 1
), tile_work AS (
    UPDATE public.almanac_slo_series_tile_work w SET attempts=0,failing_since=NULL,next_attempt_at=sqlc.arg(now)::timestamptz
    FROM changed c WHERE w.slo_id=c.slo_id AND w.definition_revision=c.definition_revision AND w.measurement_revision=c.measurement_revision
      AND w.attempts>=5 AND w.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' AND c.updated_at>w.failing_since
    RETURNING 1
), history_work AS (
    UPDATE public.almanac_slo_history_jobs j SET state='pending',attempts=0,failing_since=NULL,next_attempt_at=sqlc.arg(now)::timestamptz
    FROM changed c WHERE j.slo_id=c.slo_id AND j.definition_revision=c.definition_revision AND j.measurement_revision=c.measurement_revision
      AND j.state='failed' AND j.last_error IS DISTINCT FROM 'retention_expired'
      AND j.attempts>=5 AND j.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' AND c.updated_at>j.failing_since
    RETURNING 1
)
SELECT ((SELECT count(*) FROM current_work)+(SELECT count(*) FROM repair_work)
       +(SELECT count(*) FROM tile_work)+(SELECT count(*) FROM history_work))::bigint AS restarted;

-- name: ReadSloDerivedStates :one
-- Each active SLO is stopped (some of its work failed for seven days), failing (some of its work failed
-- five times in a row), catching up (its current values or retained history are behind) or up to date.
WITH slo AS (
    SELECT s.id,d.revision,d.measurement_revision,d.cadence_seconds,
           coalesce(source.generation,0) AS source_generation,g.id AS generation_id,g.definition_revision AS g_revision,
           g.measurement_revision AS g_measurement_revision,g.calculation_version AS g_calculation_version,g.source_generation AS g_source_generation,
           g.evaluated_through
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
), work AS (
    SELECT slo_id,definition_revision,measurement_revision,attempts,failing_since FROM public.almanac_slo_current_failures
    UNION ALL SELECT slo_id,definition_revision,measurement_revision,attempts,failing_since FROM public.almanac_slo_series_repair WHERE cursor_at<end_at
    UNION ALL SELECT slo_id,definition_revision,measurement_revision,attempts,failing_since FROM public.almanac_slo_series_tile_work
    UNION ALL SELECT slo_id,definition_revision,measurement_revision,attempts,failing_since FROM public.almanac_slo_history_jobs
        WHERE last_error IS DISTINCT FROM 'retention_expired'
    UNION ALL SELECT d.slo_id,d.revision,d.measurement_revision,dirty.attempts,dirty.failing_since
        FROM public.almanac_derived_dirty_ranges dirty
        JOIN public.almanac_slo_definitions d ON d.sli_id=dirty.sli_id AND d.measurement_revision=dirty.measurement_revision
        WHERE dirty.last_error IS DISTINCT FROM 'retention_expired'
), classified AS (
    SELECT s.id,
           bool_or(w.attempts>=5 AND w.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days') AS stopped,
           bool_or(w.attempts>=5) AS failing,
           bool_or(w.slo_id IS NOT NULL) AS queued,
           bool_or(s.generation_id IS NULL OR s.g_revision<>s.revision OR s.g_measurement_revision<>s.measurement_revision
               OR s.g_calculation_version<>sqlc.arg(calculation_version)::text OR s.g_source_generation<>s.source_generation
               OR s.evaluated_through<to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/s.cadence_seconds)*s.cadence_seconds)) AS behind
    FROM slo s
    LEFT JOIN work w ON w.slo_id=s.id AND w.definition_revision=s.revision AND w.measurement_revision=s.measurement_revision
    GROUP BY s.id
)
SELECT count(*) FILTER (WHERE coalesce(stopped,false))::bigint AS stopped,
       count(*) FILTER (WHERE NOT coalesce(stopped,false) AND coalesce(failing,false))::bigint AS failing,
       count(*) FILTER (WHERE NOT coalesce(stopped,false) AND NOT coalesce(failing,false) AND (coalesce(queued,false) OR coalesce(behind,false)))::bigint AS catching_up,
       count(*) FILTER (WHERE NOT coalesce(stopped,false) AND NOT coalesce(failing,false) AND NOT coalesce(queued,false) AND NOT coalesce(behind,false))::bigint AS up_to_date
FROM classified;

-- name: MarkDerivedHoursDirty :exec
-- A calculation found the SLI's aggregates contradicting themselves (ADR-0034): rebuild the hours it
-- read from the raw observations. Nothing is marked for an SLI with no accepted observations.
INSERT INTO public.almanac_derived_dirty_ranges
    (sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT source.sli_id,source.measurement_revision,source.generation,hour,hour+interval '1 hour',sqlc.arg(now)::timestamptz,sqlc.arg(now)::timestamptz
FROM public.almanac_sli_source_generations source
CROSS JOIN generate_series(date_trunc('hour',sqlc.arg(range_start)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
                           sqlc.arg(range_end)::timestamptz-interval '1 microsecond',interval '1 hour') hour
WHERE source.sli_id=sqlc.arg(sli_id) AND source.measurement_revision=sqlc.arg(measurement_revision)
ON CONFLICT (sli_id,measurement_revision,range_start) DO UPDATE
SET next_attempt_at=LEAST(almanac_derived_dirty_ranges.next_attempt_at,EXCLUDED.next_attempt_at),
    updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at,EXCLUDED.updated_at);
