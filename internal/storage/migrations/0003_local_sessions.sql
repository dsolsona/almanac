CREATE TABLE public.almanac_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES public.almanac_users(id),
    verifier bytea NOT NULL UNIQUE CHECK (octet_length(verifier) = 32),
    credential_revision bigint NOT NULL CHECK (credential_revision > 0),
    credential_epoch uuid NOT NULL,
    restricted boolean NOT NULL,
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    CHECK (last_seen_at >= created_at)
);

CREATE INDEX almanac_sessions_user_id_idx ON public.almanac_sessions(user_id);
