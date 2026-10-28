ALTER TABLE public.almanac_agent_configurations
    DROP CONSTRAINT almanac_agent_configurations_snapshot_check;

UPDATE public.almanac_agent_configurations
SET snapshot = snapshot || jsonb_build_object('slis', '[]'::jsonb)
WHERE NOT (snapshot ? 'slis');

ALTER TABLE public.almanac_agent_configurations
    ADD CONSTRAINT almanac_agent_configurations_snapshot_check
    CHECK (jsonb_typeof(snapshot) = 'object' AND snapshot ? 'assignments' AND
           jsonb_typeof(snapshot->'assignments') = 'array' AND snapshot ? 'slis' AND
           jsonb_typeof(snapshot->'slis') = 'array');

ALTER TABLE public.almanac_agent_execution_grants
    DROP CONSTRAINT almanac_agent_execution_grants_allowed_measurements_check;

ALTER TABLE public.almanac_agent_execution_grants
    ADD CONSTRAINT almanac_agent_execution_grants_allowed_measurements_check
    CHECK (jsonb_typeof(allowed_measurements) = 'array');

CREATE TABLE public.almanac_slis (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    display_name text NOT NULL CHECK (octet_length(display_name) BETWEEN 1 AND 512),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    measurement_revision bigint NOT NULL DEFAULT 1 CHECK (measurement_revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE public.almanac_sli_measurements (
    sli_id uuid NOT NULL REFERENCES public.almanac_slis(id),
    revision bigint NOT NULL CHECK (revision > 0),
    datasource_id uuid NOT NULL REFERENCES public.almanac_datasources(id),
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    measurement_kind text NOT NULL CHECK (octet_length(measurement_kind) BETWEEN 1 AND 64),
    component_mode text NOT NULL CHECK (octet_length(component_mode) BETWEEN 1 AND 64),
    good_query text NOT NULL CHECK (octet_length(good_query) BETWEEN 1 AND 4096),
    total_query text NOT NULL CHECK (octet_length(total_query) BETWEEN 1 AND 4096),
    effective_from timestamptz NOT NULL,
    activated_at timestamptz,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (sli_id, revision)
);

ALTER TABLE public.almanac_slis
    ADD CONSTRAINT almanac_slis_current_measurement_fk
    FOREIGN KEY (id, measurement_revision)
    REFERENCES public.almanac_sli_measurements(sli_id, revision)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX almanac_sli_measurements_agent_idx
    ON public.almanac_sli_measurements(agent_id, sli_id, revision);
CREATE INDEX almanac_sli_measurements_datasource_idx
    ON public.almanac_sli_measurements(datasource_id, sli_id);

CREATE TABLE public.almanac_sli_create_receipts (
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (actor_id, idempotency_key),
    CHECK (expires_at > created_at)
);

CREATE TABLE public.almanac_sli_coverage (
    interval_start timestamptz NOT NULL,
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    interval_end timestamptz NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('observed', 'complete_empty', 'incomplete', 'failed')),
    result_digest bytea NOT NULL CHECK (octet_length(result_digest) = 32),
    error_code text CHECK (error_code IS NULL OR error_code IN (
        'authentication', 'incomplete', 'network', 'query', 'rate_limited',
        'tls', 'timeout', 'unsupported', 'assignment_revoked', 'deadline')),
    missing_components text[] NOT NULL DEFAULT '{}',
    execution_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    observation_id uuid NOT NULL,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    session_generation bigint NOT NULL CHECK (session_generation > 0),
    configuration_revision bigint NOT NULL CHECK (configuration_revision > 0),
    lease_id text NOT NULL REFERENCES public.almanac_agent_execution_grants(lease_id),
    agent_created_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (interval_start, sli_id, measurement_revision),
    FOREIGN KEY (sli_id, measurement_revision)
        REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (interval_end > interval_start),
    CHECK (octet_length(lease_id) BETWEEN 32 AND 64),
    CHECK (missing_components <@ ARRAY['good', 'total']::text[]),
    CHECK ((outcome = 'failed' AND error_code IS NOT NULL AND cardinality(missing_components) = 0) OR
           (outcome = 'incomplete' AND error_code IS NULL AND cardinality(missing_components) BETWEEN 1 AND 2) OR
           (outcome IN ('observed', 'complete_empty') AND error_code IS NULL AND cardinality(missing_components) = 0))
) PARTITION BY RANGE (interval_start);

CREATE INDEX almanac_sli_coverage_history_idx
    ON public.almanac_sli_coverage(sli_id, interval_start DESC);

-- Failed and incomplete executions are evidence of attempts, not canonical coverage.
CREATE TABLE public.almanac_sli_execution_attempts (
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    execution_id uuid NOT NULL,
    observation_id uuid NOT NULL,
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    interval_start timestamptz NOT NULL,
    interval_end timestamptz NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('failed', 'incomplete')),
    error_code text,
    missing_components text[] NOT NULL DEFAULT '{}',
    lease_id text NOT NULL REFERENCES public.almanac_agent_execution_grants(lease_id),
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (agent_id, execution_id),
    FOREIGN KEY (sli_id, measurement_revision) REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (interval_end > interval_start)
);
CREATE INDEX almanac_sli_execution_attempts_history_idx
    ON public.almanac_sli_execution_attempts(sli_id, interval_start DESC, recorded_at DESC);

CREATE TABLE public.almanac_sli_observation_values (
    interval_start timestamptz NOT NULL,
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    good_count numeric(38, 12) NOT NULL CHECK (good_count >= 0),
    total_count numeric(38, 12) NOT NULL CHECK (total_count >= 0 AND good_count <= total_count),
    PRIMARY KEY (interval_start, sli_id, measurement_revision),
    FOREIGN KEY (interval_start, sli_id, measurement_revision)
        REFERENCES public.almanac_sli_coverage(interval_start, sli_id, measurement_revision)
        ON DELETE CASCADE
) PARTITION BY RANGE (interval_start);

CREATE INDEX almanac_sli_observation_values_history_idx
    ON public.almanac_sli_observation_values(sli_id, interval_start DESC);

CREATE TABLE public.almanac_sli_dirty_intervals (
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    interval_start timestamptz NOT NULL,
    interval_end timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (sli_id, measurement_revision, interval_start),
    FOREIGN KEY (sli_id, measurement_revision)
        REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (interval_end > interval_start)
);

CREATE TABLE public.almanac_sli_delivery_receipts (
    delivery_id uuid NOT NULL,
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    interval_start timestamptz NOT NULL,
    delivery_status text NOT NULL CHECK (delivery_status IN ('accepted', 'duplicate')),
    observation_id uuid NOT NULL,
    replay_until timestamptz NOT NULL,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    session_generation bigint NOT NULL CHECK (session_generation > 0),
    lease_id text NOT NULL REFERENCES public.almanac_agent_execution_grants(lease_id),
    configuration_revision bigint NOT NULL CHECK (configuration_revision > 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (agent_id, delivery_id),
    CHECK (replay_until > created_at)
);

CREATE INDEX almanac_sli_delivery_receipts_replay_idx
    ON public.almanac_sli_delivery_receipts(replay_until);
