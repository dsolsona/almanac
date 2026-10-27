-- name: LockLatestAgentOwnership :one
SELECT agent_id, generation, resume_verifier, acquired_at, renewed_at, expires_at,
       renewal_sequence, last_nonce, last_renewal_issued_at, last_renewal_expires_at
FROM public.almanac_agent_ownership
WHERE agent_id = sqlc.arg(agent_id)
ORDER BY generation DESC
LIMIT 1
FOR UPDATE;

-- name: InsertAgentOwnership :exec
INSERT INTO public.almanac_agent_ownership (
    agent_id, generation, resume_verifier, acquired_at, renewed_at, expires_at
)
VALUES (sqlc.arg(agent_id), sqlc.arg(generation), sqlc.arg(resume_verifier),
        sqlc.arg(acquired_at), sqlc.arg(renewed_at), sqlc.arg(expires_at));

-- name: RenewAgentOwnership :execrows
UPDATE public.almanac_agent_ownership
SET renewed_at = sqlc.arg(renewed_at),
    expires_at = sqlc.arg(expires_at),
    renewal_sequence = sqlc.arg(renewal_sequence),
    last_nonce = sqlc.arg(last_nonce),
    last_renewal_issued_at = sqlc.arg(last_renewal_issued_at),
    last_renewal_expires_at = sqlc.arg(last_renewal_expires_at)
WHERE agent_id = sqlc.arg(agent_id) AND generation = sqlc.arg(generation);
