ALTER TABLE public.almanac_agent_credentials
    ADD COLUMN retire_at timestamptz,
    ADD CONSTRAINT almanac_agent_credentials_retire_at_check
        CHECK (retire_at IS NULL OR retire_at >= issued_at);
