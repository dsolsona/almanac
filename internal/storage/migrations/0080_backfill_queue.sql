CREATE TABLE public.almanac_backfill_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sli_id uuid NOT NULL REFERENCES public.almanac_slis(id),
    requested_start timestamptz NOT NULL,
    requested_end timestamptz NOT NULL CHECK (requested_end > requested_start),
    request_key text NOT NULL CHECK (octet_length(request_key) BETWEEN 1 AND 128),
    retry_exhausted boolean NOT NULL DEFAULT false,
    requested_by uuid REFERENCES public.almanac_users(id),
    created_at timestamptz NOT NULL,
    cancelled_at timestamptz,
    UNIQUE (id, sli_id),
    UNIQUE NULLS NOT DISTINCT (sli_id, requested_by, request_key)
);

CREATE TABLE public.almanac_backfill_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL,
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    canonical_conflict boolean NOT NULL DEFAULT false,
    source_version bigint NOT NULL DEFAULT 1 CHECK (source_version > 0),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    cadence_seconds integer NOT NULL CHECK (cadence_seconds BETWEEN 15 AND 86400),
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued','leased','committed','retry-wait','unavailable','failed','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= attempts),
    next_attempt_at timestamptz NOT NULL,
    lease_until timestamptz,
    lease_session bigint CHECK (lease_session > 0),
    finished_at timestamptz,
    reason text CHECK (reason IN ('deadline','authentication','unsupported','network','timeout','query','rate_limited','expired','assignment_revoked','incomplete','tls','canonical_interval_conflict')),
    FOREIGN KEY (job_id, sli_id) REFERENCES public.almanac_backfill_jobs(id,sli_id),
    FOREIGN KEY (sli_id, measurement_revision) REFERENCES public.almanac_sli_measurements(sli_id,revision),
    UNIQUE (job_id, range_start),
    CHECK (range_end > range_start AND range_end <= range_start + interval '24 hours'),
    CHECK (mod(extract(epoch FROM range_start), cadence_seconds) = 0
       AND mod(extract(epoch FROM range_end), cadence_seconds) = 0),
    CHECK ((state = 'leased') = (lease_until IS NOT NULL)),
    CHECK ((state = 'leased') = (lease_session IS NOT NULL)),
    CHECK ((state IN ('committed','unavailable','failed','cancelled')) = (finished_at IS NOT NULL))
);

CREATE INDEX almanac_backfill_chunks_due
    ON public.almanac_backfill_chunks(agent_id,next_attempt_at,id)
    WHERE state IN ('queued','retry-wait');
CREATE INDEX almanac_backfill_chunks_expiry
    ON public.almanac_backfill_chunks(lease_until,id)
    WHERE state = 'leased';

CREATE FUNCTION public.almanac_claim_backfill_chunk(p_agent uuid, p_now timestamptz, p_session bigint DEFAULT 1)
RETURNS SETOF public.almanac_backfill_chunks
LANGUAGE sql AS $$
    WITH candidate AS (
        SELECT c.id
        FROM public.almanac_backfill_chunks c
        JOIN public.almanac_backfill_jobs j ON j.id = c.job_id
        JOIN public.almanac_sli_measurements m ON m.sli_id = c.sli_id AND m.revision = c.measurement_revision
        JOIN public.almanac_datasources d ON d.id = m.datasource_id
        WHERE c.agent_id = p_agent AND d.agent_id = p_agent AND d.historical_configuration_version = c.source_version
          AND j.cancelled_at IS NULL AND p_now < j.created_at + interval '24 hours'
          AND c.state IN ('queued','retry-wait')
          AND c.attempts < 3 AND c.next_attempt_at <= p_now
        ORDER BY c.next_attempt_at, c.id
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

CREATE FUNCTION public.almanac_reap_backfill_chunks(p_now timestamptz)
RETURNS bigint
LANGUAGE sql AS $$
    WITH expired AS (
        SELECT c.id, c.attempts >= 3 OR j.created_at + interval '24 hours' <= p_now AS exhausted,
               d.agent_id <> c.agent_id OR d.historical_configuration_version <> c.source_version AS assignment_changed
        FROM public.almanac_backfill_chunks c
        JOIN public.almanac_backfill_jobs j ON j.id = c.job_id
        JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
        JOIN public.almanac_datasources d ON d.id=m.datasource_id
        WHERE c.state IN ('queued','leased','retry-wait')
          AND (c.lease_until <= p_now OR j.created_at + interval '24 hours' <= p_now
               OR d.agent_id <> c.agent_id OR d.historical_configuration_version <> c.source_version)
        ORDER BY c.lease_until NULLS LAST, c.id
        LIMIT 1000
        FOR UPDATE OF c SKIP LOCKED
    ), updated AS (
        UPDATE public.almanac_backfill_chunks c
        SET state = CASE WHEN expired.assignment_changed THEN 'unavailable' WHEN expired.exhausted THEN 'failed' ELSE 'retry-wait' END,
            reason = CASE WHEN expired.assignment_changed THEN 'assignment_revoked' ELSE 'deadline' END, lease_until = NULL, lease_session = NULL, generation = generation + 1,
            finished_at = CASE WHEN expired.exhausted OR expired.assignment_changed THEN p_now END,
            next_attempt_at = p_now + CASE WHEN attempts <= 1 THEN interval '30 seconds' ELSE interval '2 minutes' END
        FROM expired
        WHERE c.id = expired.id
        RETURNING c.id
    )
    SELECT count(*) FROM updated;
$$;

CREATE FUNCTION public.almanac_finish_backfill_chunk(
    p_id uuid, p_agent uuid, p_generation bigint, p_now timestamptz, p_result text, p_reason text, p_session bigint DEFAULT 1)
RETURNS boolean
LANGUAGE sql AS $$
    -- The caller must use the same transaction for observation acceptance.
    WITH updated AS (
        UPDATE public.almanac_backfill_chunks c
        SET state = CASE WHEN p_result = 'retry-wait' AND c.attempts >= 3 THEN 'failed' ELSE p_result END,
            lease_until = NULL, lease_session = NULL, reason = p_reason,
            finished_at = CASE WHEN p_result <> 'retry-wait' OR c.attempts >= 3 THEN p_now END,
            next_attempt_at = p_now + CASE WHEN attempts <= 1 THEN interval '30 seconds' ELSE interval '2 minutes' END
        FROM public.almanac_backfill_jobs j, public.almanac_sli_measurements m, public.almanac_datasources d
        WHERE c.id = p_id AND c.agent_id = p_agent AND c.generation = p_generation
          AND c.state = 'leased' AND c.lease_session = p_session AND p_now < c.lease_until
          AND j.id = c.job_id AND j.cancelled_at IS NULL AND p_now < j.created_at + interval '24 hours'
          AND m.sli_id = c.sli_id AND m.revision = c.measurement_revision
          AND d.id = m.datasource_id AND d.agent_id = p_agent AND d.historical_configuration_version = c.source_version
          AND p_result IN ('committed','retry-wait','unavailable','failed')
        RETURNING c.id
    )
    SELECT EXISTS (SELECT 1 FROM updated);
$$;

CREATE FUNCTION public.almanac_cancel_backfill_job(p_id uuid, p_now timestamptz)
RETURNS bigint
LANGUAGE sql AS $$
    WITH job AS (
        UPDATE public.almanac_backfill_jobs j
        SET cancelled_at = p_now
        WHERE j.id = p_id AND j.cancelled_at IS NULL
          AND EXISTS (SELECT 1 FROM public.almanac_backfill_chunks c WHERE c.job_id = j.id
                      AND c.state IN ('queued','leased','retry-wait'))
        RETURNING j.id
    ), cancelled AS (
        UPDATE public.almanac_backfill_chunks c
        SET state = 'cancelled', generation = generation + 1, lease_until = NULL, lease_session = NULL, finished_at = p_now
        FROM job
        WHERE c.job_id = job.id AND c.state IN ('queued','leased','retry-wait')
        RETURNING c.id
    )
    SELECT count(*) FROM cancelled;
$$;

ALTER TABLE public.almanac_sli_observations
    ALTER COLUMN lease_id DROP NOT NULL,
    ADD COLUMN backfill_chunk_id uuid REFERENCES public.almanac_backfill_chunks(id),
    ADD CONSTRAINT almanac_observation_execution_provenance
        CHECK ((lease_id IS NOT NULL) <> (backfill_chunk_id IS NOT NULL));

CREATE INDEX almanac_sli_observations_backfill_chunk_idx
    ON public.almanac_sli_observations(backfill_chunk_id) WHERE backfill_chunk_id IS NOT NULL;

CREATE INDEX almanac_backfill_jobs_sli
    ON public.almanac_backfill_jobs(sli_id,created_at DESC,id);
CREATE INDEX almanac_backfill_chunks_sli_range
    ON public.almanac_backfill_chunks(sli_id,measurement_revision,range_start,range_end);

CREATE TABLE public.almanac_backfill_scan_cursor (
    sweep text PRIMARY KEY CHECK (sweep IN ('recent','retained')),
    sli_id uuid,
    next_interval timestamptz
);
INSERT INTO public.almanac_backfill_scan_cursor(sweep) VALUES ('recent'), ('retained');
