-- name: LockPasswordRotationInstallation :one
SELECT credential_epoch, bootstrap_admin_id, onboarding_completed_at
FROM public.almanac_installation
WHERE singleton
FOR UPDATE;

-- name: SessionVerifierExists :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_sessions WHERE verifier = sqlc.arg(verifier)
);

-- name: RotateLocalPassword :execrows
UPDATE public.almanac_users
SET password_verifier = sqlc.arg(password_verifier),
    credential_revision = credential_revision + 1,
    must_change_password = false,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(user_id)
  AND credential_revision < 9223372036854775807;

-- name: DeleteUserSessions :exec
DELETE FROM public.almanac_sessions WHERE user_id = sqlc.arg(user_id);

-- name: CompletePasswordRotationOnboarding :execrows
UPDATE public.almanac_installation
SET onboarding_completed_at = sqlc.arg(onboarding_completed_at)
WHERE singleton AND onboarding_completed_at IS NULL;

-- name: InsertPasswordRotationAudit :exec
INSERT INTO public.almanac_audit_events (
    actor, subject_id, action, outcome, occurred_at, request_id
)
VALUES (sqlc.arg(actor), sqlc.arg(subject_id), sqlc.arg(action), 'success',
        sqlc.arg(occurred_at), sqlc.arg(request_id));
