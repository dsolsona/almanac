CREATE TABLE public.almanac_agent_ownership (
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    generation bigint NOT NULL CHECK (generation > 0),
    resume_verifier bytea NOT NULL CHECK (octet_length(resume_verifier) = 32),
    acquired_at timestamptz NOT NULL,
    renewed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    renewal_sequence bigint NOT NULL DEFAULT 0 CHECK (renewal_sequence >= 0),
    last_nonce bytea,
    last_renewal_issued_at timestamptz,
    last_renewal_expires_at timestamptz,
    PRIMARY KEY (agent_id, generation),
    CHECK (renewed_at >= acquired_at),
    CHECK (expires_at > renewed_at),
    CHECK ((renewal_sequence = 0 AND last_nonce IS NULL AND last_renewal_issued_at IS NULL AND
            last_renewal_expires_at IS NULL) OR
           (renewal_sequence > 0 AND last_nonce IS NOT NULL AND octet_length(last_nonce) = 32 AND
            last_nonce <> decode(repeat('00', 32), 'hex') AND
            last_renewal_issued_at IS NOT NULL AND last_renewal_expires_at IS NOT NULL AND
            last_renewal_issued_at >= acquired_at AND last_renewal_issued_at = renewed_at AND
            last_renewal_expires_at = expires_at AND last_renewal_expires_at > last_renewal_issued_at))
);

CREATE INDEX almanac_agent_ownership_latest_idx
    ON public.almanac_agent_ownership(agent_id, generation DESC);
