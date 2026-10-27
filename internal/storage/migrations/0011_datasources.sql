ALTER TABLE public.almanac_agent_configurations
    DROP CONSTRAINT almanac_agent_configurations_snapshot_check;

ALTER TABLE public.almanac_agent_configurations
    ADD CONSTRAINT almanac_agent_configurations_snapshot_check
    CHECK (jsonb_typeof(snapshot) = 'object' AND snapshot ? 'assignments' AND
           jsonb_typeof(snapshot->'assignments') = 'array');

CREATE TABLE public.almanac_datasources (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    display_name text NOT NULL CHECK (octet_length(display_name) BETWEEN 1 AND 256),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    provider text NOT NULL CHECK (provider = 'prometheus'),
    configuration_format text NOT NULL CHECK (configuration_format = 'prometheus-http-v1'),
    endpoint text NOT NULL CHECK (octet_length(endpoint) BETWEEN 1 AND 2048),
    tls jsonb NOT NULL CHECK (jsonb_typeof(tls) = 'object'),
    authentication_mode text NOT NULL CHECK (authentication_mode IN ('none', 'basic', 'bearer')),
    authentication_username text,
    password_envelope jsonb,
    password_revision bigint NOT NULL DEFAULT 0 CHECK (password_revision >= 0),
    token_envelope jsonb,
    token_revision bigint NOT NULL DEFAULT 0 CHECK (token_revision >= 0),
    polling_interval_seconds integer NOT NULL CHECK (polling_interval_seconds BETWEEN 15 AND 86400),
    settling_delay_seconds integer NOT NULL DEFAULT 60 CHECK (settling_delay_seconds = 60),
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((password_envelope IS NULL AND password_revision >= 0) OR
           (password_envelope IS NOT NULL AND password_revision > 0)),
    CHECK ((token_envelope IS NULL AND token_revision >= 0) OR
           (token_envelope IS NOT NULL AND token_revision > 0)),
    CHECK ((authentication_mode = 'none' AND authentication_username IS NULL AND password_envelope IS NULL AND token_envelope IS NULL) OR
           (authentication_mode = 'basic' AND authentication_username IS NOT NULL AND octet_length(authentication_username) BETWEEN 1 AND 256 AND password_envelope IS NOT NULL AND token_envelope IS NULL) OR
           (authentication_mode = 'bearer' AND authentication_username IS NULL AND password_envelope IS NULL AND token_envelope IS NOT NULL))
);

CREATE INDEX almanac_datasources_agent_id_idx ON public.almanac_datasources(agent_id, id);

CREATE TABLE public.almanac_diagnostics (
    id uuid PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    datasource_id uuid REFERENCES public.almanac_datasources(id),
    session_generation bigint NOT NULL CHECK (session_generation > 0),
    operation text NOT NULL CHECK (operation IN ('connection_test', 'query_preview')),
    state text NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown')),
    request jsonb NOT NULL CHECK (jsonb_typeof(request) = 'object'),
    result jsonb,
    error_code text,
    message text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    deadline_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (deadline_at > created_at AND expires_at >= deadline_at),
    CHECK (message IS NULL OR octet_length(message) <= 512)
);

CREATE INDEX almanac_diagnostics_expiry_idx ON public.almanac_diagnostics(expires_at);
