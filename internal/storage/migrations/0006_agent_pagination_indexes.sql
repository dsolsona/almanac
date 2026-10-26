CREATE INDEX almanac_agents_created_id_idx
    ON public.almanac_agents (created_at, id);

CREATE INDEX almanac_agent_credentials_agent_issued_id_idx
    ON public.almanac_agent_credentials (agent_id, issued_at, id);
