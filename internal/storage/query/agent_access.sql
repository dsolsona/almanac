-- name: FindAgentAccessCredentialAgent :one
SELECT agent_id FROM public.almanac_agent_credentials
WHERE id = sqlc.arg(credential_id);

-- name: LockAgentAccessAgent :one
SELECT id, enabled FROM public.almanac_agents
WHERE id = sqlc.arg(agent_id) FOR UPDATE;

-- name: LockAgentAccessCredential :one
SELECT id, agent_id, verifier, credential_epoch, issued_at, expires_at,
       revoked_at, retire_at
FROM public.almanac_agent_credentials
WHERE id = sqlc.arg(credential_id) AND agent_id = sqlc.arg(agent_id)
FOR UPDATE;
