-- name: LockAgentSessionRead :one
SELECT id, user_id, verifier, credential_revision, credential_epoch,
       restricted, created_at, last_seen_at
FROM public.almanac_sessions
WHERE verifier = sqlc.arg(verifier)
FOR SHARE;

-- name: GetAgent :one
SELECT id, name, display_name, enabled, version, created_at, updated_at, deleted_at
FROM public.almanac_agents WHERE id = sqlc.arg(agent_id) AND deleted_at IS NULL FOR SHARE;

-- name: ListAgents :many
SELECT id, name, display_name, enabled, version, created_at, updated_at, deleted_at
FROM public.almanac_agents
WHERE deleted_at IS NULL AND (created_at, id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY created_at, id LIMIT sqlc.arg(row_limit);

-- name: ListAgentCredentials :many
SELECT id, agent_id, issued_at, expires_at, revoked_at, retire_at
FROM public.almanac_agent_credentials
WHERE agent_id = sqlc.arg(agent_id)
  AND (issued_at, id) > (sqlc.arg(after_issued_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY issued_at, id LIMIT sqlc.arg(row_limit);

-- name: AgentHasAssignedDatasources :one
SELECT EXISTS (SELECT 1 FROM public.almanac_datasources WHERE agent_id = sqlc.arg(agent_id));
