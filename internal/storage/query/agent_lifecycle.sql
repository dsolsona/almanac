-- name: LockLifecycleAgent :one
SELECT id, name, display_name, enabled, version, created_at, updated_at
FROM public.almanac_agents WHERE id = sqlc.arg(agent_id) FOR UPDATE;

-- name: LockLifecycleCredential :one
SELECT id, agent_id, issued_at, expires_at, revoked_at, retire_at
FROM public.almanac_agent_credentials
WHERE id = sqlc.arg(credential_id) AND agent_id = sqlc.arg(agent_id) FOR UPDATE;

-- name: LockUnrevokedAgentCredentials :one
WITH locked AS MATERIALIZED (
    SELECT id, issued_at FROM public.almanac_agent_credentials
    WHERE agent_id = sqlc.arg(agent_id) AND revoked_at IS NULL
    ORDER BY id FOR UPDATE
)
SELECT count(*)::bigint AS credential_count
FROM locked;

-- name: HasFutureUnrevokedAgentCredential :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_agent_credentials
    WHERE agent_id = sqlc.arg(agent_id) AND revoked_at IS NULL AND issued_at > sqlc.arg(now)
);

-- name: FindAgentLifecycleReceipt :one
SELECT digest, result, expires_at
FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = sqlc.arg(operation)
  AND idempotency_key = sqlc.arg(idempotency_key) FOR UPDATE;

-- name: DeleteExpiredAgentLifecycleReceipt :execrows
DELETE FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = sqlc.arg(operation)
  AND idempotency_key = sqlc.arg(idempotency_key) AND expires_at <= sqlc.arg(now);

-- name: RevokeAgentCredential :one
UPDATE public.almanac_agent_credentials SET revoked_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(credential_id) AND agent_id = sqlc.arg(agent_id) AND revoked_at IS NULL
RETURNING id, agent_id, issued_at, expires_at, revoked_at, retire_at;

-- name: UpdateLifecycleAgent :one
UPDATE public.almanac_agents SET enabled = sqlc.arg(enabled), version = version + 1,
       updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(agent_id) AND version < 9223372036854775807
RETURNING id, name, display_name, enabled, version, created_at, updated_at;

-- name: DisableAgent :one
UPDATE public.almanac_agents SET enabled = false, version = version + 1,
       updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(agent_id) AND version < 9223372036854775807
RETURNING id, name, display_name, enabled, version, created_at, updated_at;

-- name: InsertLifecycleAudit :exec
INSERT INTO public.almanac_audit_events(actor, subject_id, action, outcome, occurred_at, request_id)
VALUES (sqlc.arg(actor), sqlc.arg(subject_id), sqlc.arg(action), 'success', sqlc.arg(occurred_at), sqlc.arg(request_id));

-- name: RevokeAndAuditUnrevokedAgentCredentials :execrows
WITH changed AS (
    UPDATE public.almanac_agent_credentials SET revoked_at = sqlc.arg(revoked_at)
    WHERE agent_id = sqlc.arg(agent_id) AND revoked_at IS NULL
    RETURNING id
)
INSERT INTO public.almanac_audit_events(actor, subject_id, action, outcome, occurred_at, request_id)
SELECT sqlc.arg(actor), id, 'agent.credential.revoked', 'success', sqlc.arg(occurred_at), sqlc.arg(request_id)
FROM changed;

-- name: InsertAgentLifecycleReceipt :exec
INSERT INTO public.almanac_management_receipts(actor_id, operation, idempotency_key, digest, result, created_at, expires_at)
VALUES (sqlc.arg(actor_id), sqlc.arg(operation), sqlc.arg(idempotency_key), sqlc.arg(digest),
        sqlc.arg(result), sqlc.arg(created_at), sqlc.arg(expires_at));
