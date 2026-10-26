-- name: LockActiveRotationCredentials :many
SELECT id, agent_id, issued_at, expires_at, revoked_at, retire_at
FROM public.almanac_agent_credentials
WHERE agent_id = sqlc.arg(agent_id)
  AND credential_epoch = sqlc.arg(credential_epoch)
  AND revoked_at IS NULL
  AND issued_at <= sqlc.arg(now)
  AND expires_at > sqlc.arg(now)
  AND (retire_at IS NULL OR retire_at > sqlc.arg(now))
ORDER BY id
LIMIT 2
FOR UPDATE;

-- name: InsertRotatedAgentCredential :one
INSERT INTO public.almanac_agent_credentials
    (id, agent_id, verifier, credential_epoch, issued_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(agent_id), sqlc.arg(verifier), sqlc.arg(credential_epoch),
        sqlc.arg(issued_at), sqlc.arg(expires_at))
RETURNING id, agent_id, issued_at, expires_at, revoked_at, retire_at;

-- name: RetireAgentCredential :execrows
UPDATE public.almanac_agent_credentials
SET retire_at = sqlc.arg(retire_at)
WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id)
  AND revoked_at IS NULL AND (retire_at IS NULL OR retire_at >= sqlc.arg(retire_at));
