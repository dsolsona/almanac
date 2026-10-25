-- name: GetInitialOwnerState :one
SELECT bootstrap_admin_id, onboarding_completed_at
FROM public.almanac_installation
WHERE singleton;

-- name: LockInitialOwnerState :one
SELECT bootstrap_admin_id, onboarding_completed_at
FROM public.almanac_installation
WHERE singleton
FOR UPDATE;

-- name: InsertInitialOwner :one
INSERT INTO public.almanac_users (
    login, password_verifier, role, enabled, must_change_password,
    credential_revision, created_at, updated_at
)
VALUES ($1, $2, 'administrator', true, true, 1, $3, $3)
RETURNING id;

-- name: InsertInitialOwnerAudit :exec
INSERT INTO public.almanac_audit_events (
    actor, subject_id, action, outcome, occurred_at, request_id
)
VALUES ('system', $1, 'identity.bootstrap.provision', 'success', $2, $3);

-- name: MarkInitialOwnerProvisioned :exec
UPDATE public.almanac_installation
SET bootstrap_admin_id = $1, bootstrapped_at = $2
WHERE singleton;
