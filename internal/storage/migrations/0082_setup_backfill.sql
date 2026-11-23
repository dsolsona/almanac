ALTER TABLE public.almanac_sli_measurements
    ADD COLUMN initial_live_from timestamptz,
    ADD CONSTRAINT almanac_sli_initial_live_from CHECK (
        initial_live_from IS NULL OR (revision=1 AND initial_live_from > effective_from)
    );
ALTER TABLE public.almanac_backfill_jobs
    ADD COLUMN earlier_history_from timestamptz,
    ADD COLUMN previous_valid_from timestamptz,
    ADD COLUMN earlier_history_context text,
    ADD CONSTRAINT almanac_backfill_earlier_history CHECK (
        (earlier_history_from IS NULL AND previous_valid_from IS NULL AND earlier_history_context IS NULL) OR
        (requested_by IS NOT NULL
         AND earlier_history_from IS NOT NULL AND previous_valid_from IS NOT NULL
         AND earlier_history_context IS NOT NULL AND earlier_history_context ~ '^[0-9a-f]{64}$'
         AND requested_start <= earlier_history_from AND earlier_history_from < previous_valid_from
         AND previous_valid_from <= requested_end)
    );

CREATE TABLE public.almanac_backfill_source_cooldowns (
    datasource_id uuid PRIMARY KEY REFERENCES public.almanac_datasources(id) ON DELETE CASCADE,
    source_version bigint NOT NULL,
    retry_at timestamptz NOT NULL
);

CREATE OR REPLACE FUNCTION public.almanac_claim_backfill_chunk(p_agent uuid, p_now timestamptz, p_session bigint DEFAULT 1)
RETURNS SETOF public.almanac_backfill_chunks
LANGUAGE sql AS $$
    WITH candidate AS (
        SELECT c.id
        FROM public.almanac_backfill_chunks c
        JOIN public.almanac_backfill_jobs j ON j.id = c.job_id
        JOIN public.almanac_sli_measurements m ON m.sli_id = c.sli_id AND m.revision = c.measurement_revision
        JOIN public.almanac_datasources d ON d.id = m.datasource_id
        WHERE c.agent_id = p_agent AND d.agent_id = p_agent AND d.historical_configuration_version = c.source_version
          AND NOT EXISTS (
              SELECT 1 FROM public.almanac_backfill_source_cooldowns cooldown
              WHERE cooldown.datasource_id=d.id AND cooldown.source_version=d.historical_configuration_version
                AND cooldown.retry_at > p_now
          )
          AND j.cancelled_at IS NULL AND p_now < j.created_at + interval '24 hours'
          AND c.state IN ('queued','retry-wait')
          AND c.attempts < 3 AND c.next_attempt_at <= p_now
        ORDER BY c.next_attempt_at, c.range_start, c.id
        FOR UPDATE OF c SKIP LOCKED
        LIMIT 1
    )
    UPDATE public.almanac_backfill_chunks c
    SET state = 'leased', attempts = attempts + 1, generation = generation + 1,
        lease_until = p_now + interval '2 minutes', lease_session = p_session
    FROM candidate
    WHERE c.id = candidate.id
    RETURNING c.*;
$$;
