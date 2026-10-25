-- name: ReadLocalIdentity :one
SELECT u.id, u.login, u.role, i.credential_epoch, u.password_verifier,
       u.enabled, u.must_change_password, u.credential_revision
FROM public.almanac_users u
LEFT JOIN public.almanac_installation i ON i.singleton
WHERE lower(u.login) = lower(sqlc.arg(login));

-- name: LockSessionInstallation :one
SELECT credential_epoch, bootstrap_admin_id, onboarding_completed_at
FROM public.almanac_installation
WHERE singleton
FOR SHARE;

-- name: LockSessionUser :one
SELECT id, login, role, password_verifier, enabled, must_change_password,
       credential_revision
FROM public.almanac_users
WHERE id = sqlc.arg(user_id)
FOR UPDATE;

-- name: FindSessionUserID :one
SELECT user_id FROM public.almanac_sessions WHERE verifier = sqlc.arg(verifier);

-- name: LockSession :one
SELECT id, user_id, verifier, credential_revision, credential_epoch,
       restricted, created_at, last_seen_at
FROM public.almanac_sessions
WHERE verifier = sqlc.arg(verifier)
FOR UPDATE;

-- name: InsertSession :one
INSERT INTO public.almanac_sessions (
    user_id, verifier, credential_revision, credential_epoch, restricted,
    created_at, last_seen_at
)
VALUES ($1, $2, $3, $4, $5, $6, $6)
RETURNING id;

-- name: TouchSession :execrows
UPDATE public.almanac_sessions
SET last_seen_at = GREATEST(last_seen_at, sqlc.arg(last_seen_at)::timestamptz)
WHERE id = sqlc.arg(id);

-- name: DeleteSession :execrows
DELETE FROM public.almanac_sessions WHERE id = sqlc.arg(id);

-- name: InsertSessionAudit :exec
INSERT INTO public.almanac_audit_events (
    actor, subject_id, action, outcome, occurred_at, request_id
)
VALUES (sqlc.arg(actor), sqlc.arg(subject_id), sqlc.arg(action), 'success',
        sqlc.arg(occurred_at), sqlc.arg(request_id));
