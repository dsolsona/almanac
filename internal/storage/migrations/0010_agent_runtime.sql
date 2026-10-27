CREATE TABLE public.almanac_agent_configurations (
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    revision bigint NOT NULL CHECK (revision > 0),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object' AND
        snapshot ? 'assignments' AND jsonb_typeof(snapshot->'assignments') = 'array' AND
        snapshot->'assignments' = '[]'::jsonb),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (agent_id, revision)
);

CREATE TABLE public.almanac_agent_execution_grants (
    lease_id text PRIMARY KEY CHECK (octet_length(lease_id) BETWEEN 32 AND 64),
    agent_id uuid NOT NULL,
    generation bigint NOT NULL,
    configuration_revision bigint NOT NULL,
    allowed_measurements jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (
        jsonb_typeof(allowed_measurements) = 'array' AND allowed_measurements = '[]'::jsonb),
    valid_from timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    renewal_sequence bigint NOT NULL DEFAULT 0 CHECK (renewal_sequence >= 0),
    request_nonce bytea,
    retain_until timestamptz NOT NULL,
    UNIQUE (agent_id, generation, renewal_sequence),
    FOREIGN KEY (agent_id, generation)
        REFERENCES public.almanac_agent_ownership(agent_id, generation),
    FOREIGN KEY (agent_id, configuration_revision)
        REFERENCES public.almanac_agent_configurations(agent_id, revision),
    CHECK (valid_until > valid_from AND retain_until >= valid_until + interval '7 days'),
    CHECK ((renewal_sequence = 0 AND request_nonce IS NULL) OR
        (renewal_sequence > 0 AND request_nonce IS NOT NULL AND octet_length(request_nonce) = 32 AND
         request_nonce <> decode(repeat('00', 32), 'hex')))
);

CREATE INDEX almanac_agent_execution_grants_retention_idx
    ON public.almanac_agent_execution_grants(agent_id, retain_until);

ALTER TABLE public.almanac_agent_ownership
    ADD COLUMN current_lease_id text;

CREATE TABLE public.almanac_agent_runtime_status (
    agent_id uuid PRIMARY KEY REFERENCES public.almanac_agents(id),
    generation bigint NOT NULL CHECK (generation > 0),
    protocol text NOT NULL CHECK (octet_length(protocol) BETWEEN 1 AND 64),
    software_version text NOT NULL CHECK (octet_length(software_version) BETWEEN 1 AND 128),
    capabilities jsonb NOT NULL CHECK (jsonb_typeof(capabilities) = 'array'),
    last_heartbeat_at timestamptz,
    desired_configuration_revision bigint NOT NULL CHECK (desired_configuration_revision > 0),
    applied_configuration_revision bigint NOT NULL DEFAULT 0 CHECK (applied_configuration_revision >= 0),
    scheduler_health text NOT NULL CHECK (scheduler_health IN ('starting', 'ready', 'degraded', 'stopped')),
    collection_progress text NOT NULL CHECK (collection_progress IN ('not_started', 'idle', 'running', 'blocked')),
    last_attempt_at timestamptz,
    last_successful_query_at timestamptz,
    next_expected_execution_at timestamptz,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (agent_id, generation)
        REFERENCES public.almanac_agent_ownership(agent_id, generation)
);
