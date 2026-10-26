CREATE TABLE public.almanac_agents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    display_name text NOT NULL CHECK (octet_length(display_name) BETWEEN 1 AND 256),
    enabled boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE public.almanac_agent_credentials (
    id uuid PRIMARY KEY,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    verifier bytea NOT NULL CHECK (octet_length(verifier) = 32),
    credential_epoch uuid NOT NULL,
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > issued_at),
    CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);
CREATE INDEX almanac_agent_credentials_agent_id_idx ON public.almanac_agent_credentials(agent_id);

CREATE TABLE public.almanac_management_receipts (
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (actor_id, operation, idempotency_key),
    CHECK (expires_at > created_at)
);
CREATE INDEX almanac_management_receipts_expiry_idx ON public.almanac_management_receipts(expires_at);
