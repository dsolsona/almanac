-- name: FindAgentSessionUserID :one
SELECT user_id FROM public.almanac_sessions WHERE verifier = sqlc.arg(verifier);

-- name: LockAgentSession :one
SELECT id, user_id, verifier, credential_revision, credential_epoch,
       restricted, created_at, last_seen_at
FROM public.almanac_sessions
WHERE verifier = sqlc.arg(verifier)
FOR UPDATE;

-- name: FindAgentReceipt :one
SELECT digest, result, expires_at
FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = 'agent.create'
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: DeleteExpiredAgentReceipt :execrows
DELETE FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = 'agent.create'
  AND idempotency_key = sqlc.arg(idempotency_key) AND expires_at <= sqlc.arg(now);

-- name: InsertAgent :one
INSERT INTO public.almanac_agents (name, display_name, enabled, version, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(display_name), true, 1, sqlc.arg(created_at), sqlc.arg(created_at))
RETURNING id, name, display_name, enabled, version, created_at, updated_at, deleted_at;

-- name: InsertAgentCredential :one
INSERT INTO public.almanac_agent_credentials (id, agent_id, verifier, credential_epoch, issued_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(agent_id), sqlc.arg(verifier), sqlc.arg(credential_epoch),
        sqlc.arg(issued_at), sqlc.arg(expires_at))
RETURNING id, agent_id, issued_at, expires_at;

-- name: InsertAgentAudit :exec
INSERT INTO public.almanac_audit_events (actor, subject_id, action, outcome, occurred_at, request_id)
VALUES (sqlc.arg(actor), sqlc.arg(subject_id), sqlc.arg(action), 'success', sqlc.arg(occurred_at), sqlc.arg(request_id));

-- name: InsertAgentReceipt :exec
INSERT INTO public.almanac_management_receipts
(actor_id, operation, idempotency_key, digest, result, created_at, expires_at)
VALUES (sqlc.arg(actor_id), 'agent.create', sqlc.arg(idempotency_key), sqlc.arg(digest),
        sqlc.arg(result), sqlc.arg(created_at), sqlc.arg(expires_at));
