ALTER TABLE public.almanac_users
 ADD COLUMN full_name text NOT NULL DEFAULT '' CHECK (char_length(full_name) <= 160),
 ADD COLUMN email text CHECK (octet_length(email) BETWEEN 3 AND 254),
 ADD COLUMN login_is_alias boolean NOT NULL DEFAULT true,
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 ADD COLUMN last_sign_in timestamptz,
 ADD COLUMN sign_in_history_known boolean NOT NULL DEFAULT false,
 ADD COLUMN password_login_suspended boolean NOT NULL DEFAULT false,
 ADD COLUMN deleted_at timestamptz;

UPDATE public.almanac_users SET full_name = left(login, 160);
CREATE UNIQUE INDEX almanac_users_email_lower_unique ON public.almanac_users (lower(email));

CREATE TABLE public.almanac_user_challenges (
 verifier bytea PRIMARY KEY CHECK (octet_length(verifier) = 32),
 user_id uuid NOT NULL UNIQUE REFERENCES public.almanac_users(id),
 purpose text NOT NULL CHECK (purpose IN ('setup','reset')),
 credential_revision bigint NOT NULL,
 credential_epoch uuid NOT NULL,
 expires_at timestamptz NOT NULL
);

CREATE TABLE public.almanac_user_receipts (
 actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
 command_key text NOT NULL CHECK (octet_length(command_key) BETWEEN 1 AND 128),
 digest bytea NOT NULL CHECK (octet_length(digest) = 32),
 result jsonb NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY (actor_id, command_key)
);
