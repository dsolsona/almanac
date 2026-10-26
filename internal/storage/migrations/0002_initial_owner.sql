CREATE TABLE public.almanac_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login text NOT NULL CHECK (octet_length(login) BETWEEN 1 AND 256),
    password_verifier text,
    role text NOT NULL CHECK (role IN ('viewer', 'editor', 'administrator')),
    enabled boolean NOT NULL,
    must_change_password boolean NOT NULL,
    credential_revision bigint NOT NULL DEFAULT 1 CHECK (credential_revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX almanac_users_login_lower_unique
    ON public.almanac_users (lower(login));

CREATE TABLE public.almanac_audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor text NOT NULL,
    subject_id uuid NOT NULL,
    action text NOT NULL,
    outcome text NOT NULL,
    occurred_at timestamptz NOT NULL,
    request_id text NOT NULL
);

ALTER TABLE public.almanac_installation
    ADD COLUMN bootstrap_admin_id uuid REFERENCES public.almanac_users(id),
    ADD COLUMN bootstrapped_at timestamptz,
    ADD COLUMN onboarding_completed_at timestamptz,
    ADD CONSTRAINT almanac_installation_bootstrap_pair CHECK (
        (bootstrap_admin_id IS NULL) = (bootstrapped_at IS NULL)
    ),
    ADD CONSTRAINT almanac_installation_onboarding_requires_bootstrap CHECK (
        onboarding_completed_at IS NULL OR bootstrap_admin_id IS NOT NULL
    );
