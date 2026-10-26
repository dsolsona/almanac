CREATE INDEX almanac_agent_credentials_rotation_cutoff_idx
ON public.almanac_agent_credentials
    (agent_id, credential_epoch, LEAST(expires_at, retire_at), id)
WHERE revoked_at IS NULL;
