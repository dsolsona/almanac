-- name: GetManagedUser :one
SELECT * FROM public.almanac_users WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockManagedUser :one
SELECT * FROM public.almanac_users WHERE id = sqlc.arg(id) AND deleted_at IS NULL FOR UPDATE;

-- name: ListManagedUsers :many
SELECT * FROM public.almanac_users
WHERE deleted_at IS NULL
 AND (sqlc.arg(search)::text = '' OR strpos(lower(coalesce(nullif(full_name,''),login) || ' ' || coalesce(email,'')), lower(sqlc.arg(search)::text)) > 0)
 AND (sqlc.arg(role_filter)::text = '' OR role = sqlc.arg(role_filter))
 AND (sqlc.arg(status_filter)::text = '' OR CASE WHEN NOT enabled THEN 'disabled' WHEN password_login_suspended THEN 'resetting' WHEN password_verifier IS NULL THEN 'pending' ELSE 'active' END = sqlc.arg(status_filter))
 AND (sqlc.arg(after_name)::text = '' OR (lower(coalesce(nullif(full_name,''),login)),id) > (lower(sqlc.arg(after_name)::text),sqlc.arg(after_id)::uuid))
ORDER BY lower(coalesce(nullif(full_name,''),login)), id
LIMIT sqlc.arg(row_limit);

-- name: CountManagedUsers :one
SELECT count(*) FROM public.almanac_users
WHERE deleted_at IS NULL
 AND (sqlc.arg(search)::text = '' OR strpos(lower(coalesce(nullif(full_name,''),login) || ' ' || coalesce(email,'')), lower(sqlc.arg(search)::text)) > 0)
 AND (sqlc.arg(role_filter)::text = '' OR role = sqlc.arg(role_filter))
 AND (sqlc.arg(status_filter)::text = '' OR CASE WHEN NOT enabled THEN 'disabled' WHEN password_login_suspended THEN 'resetting' WHEN password_verifier IS NULL THEN 'pending' ELSE 'active' END = sqlc.arg(status_filter));

-- name: CountUsableAdmins :one
SELECT count(*) FROM public.almanac_users WHERE deleted_at IS NULL AND enabled AND role='administrator' AND password_verifier IS NOT NULL AND NOT password_login_suspended;

-- name: UserEmailInUse :one
SELECT EXISTS (SELECT 1 FROM public.almanac_users WHERE id <> sqlc.arg(except_id)::uuid AND (lower(email)=lower(sqlc.arg(email)::text) OR lower(login)=lower(sqlc.arg(email)::text)));

-- name: InsertManagedUser :one
INSERT INTO public.almanac_users (login,full_name,email,login_is_alias,role,enabled,must_change_password,sign_in_history_known,created_at,updated_at)
VALUES (sqlc.arg(email),sqlc.arg(full_name),sqlc.arg(email),false,sqlc.arg(role),true,false,true,sqlc.arg(now),sqlc.arg(now)) RETURNING *;

-- name: UpdateManagedUser :one
UPDATE public.almanac_users SET full_name=sqlc.arg(full_name), email=sqlc.arg(email), login=sqlc.arg(login), role=sqlc.arg(role), enabled=sqlc.arg(enabled),
 password_login_suspended=sqlc.arg(suspended),deleted_at=sqlc.narg(deleted_at),
 credential_revision=credential_revision+sqlc.arg(invalidate)::bigint,version=version+1,updated_at=sqlc.arg(now)
WHERE id=sqlc.arg(id) RETURNING *;

-- name: RecordUserSignIn :exec
UPDATE public.almanac_users SET last_sign_in=sqlc.arg(now),sign_in_history_known=true WHERE id=sqlc.arg(id);

-- name: DeleteUserChallenges :exec
DELETE FROM public.almanac_user_challenges WHERE user_id=sqlc.arg(user_id);

-- name: InsertUserChallenge :exec
INSERT INTO public.almanac_user_challenges (verifier,user_id,purpose,credential_revision,credential_epoch,expires_at) VALUES ($1,$2,$3,$4,$5,$6);

-- name: FindUserChallenge :one
SELECT * FROM public.almanac_user_challenges WHERE verifier=sqlc.arg(verifier);

-- name: RedeemUserPassword :one
UPDATE public.almanac_users SET password_verifier=sqlc.arg(password_verifier),password_login_suspended=false,must_change_password=false,
 credential_revision=credential_revision+1,version=version+1,updated_at=sqlc.arg(now)
WHERE id=sqlc.arg(id) RETURNING *;

-- name: FindUserReceipt :one
SELECT digest,result FROM public.almanac_user_receipts WHERE actor_id=sqlc.arg(actor_id) AND command_key=sqlc.arg(command_key) AND expires_at>sqlc.arg(now);

-- name: DeleteExpiredUserReceipts :exec
DELETE FROM public.almanac_user_receipts WHERE expires_at <= sqlc.arg(now);

-- name: InsertUserReceipt :exec
INSERT INTO public.almanac_user_receipts (actor_id,command_key,digest,result,expires_at) VALUES ($1,$2,$3,$4,$5);
