CREATE TABLE public.almanac_login_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_key bytea NOT NULL CHECK (octet_length(account_key) = 32),
    source_key bytea NOT NULL CHECK (octet_length(source_key) = 32),
    started_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'failed')),
    CHECK (expires_at > started_at)
);

CREATE INDEX almanac_login_attempts_account_expiry_idx
    ON public.almanac_login_attempts (account_key, expires_at);
CREATE INDEX almanac_login_attempts_source_expiry_idx
    ON public.almanac_login_attempts (source_key, expires_at);
CREATE INDEX almanac_login_attempts_expiry_idx
    ON public.almanac_login_attempts (expires_at);
