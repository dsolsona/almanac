CREATE INDEX almanac_agent_ownership_proof_idx
    ON public.almanac_agent_ownership(agent_id, resume_verifier);
