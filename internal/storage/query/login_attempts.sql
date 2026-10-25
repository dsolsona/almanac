-- name: LoginAttemptKeys :one
SELECT sha256(convert_to(lower(sqlc.arg(login)::text), 'UTF8')) AS account_key,
       sha256(convert_to(sqlc.arg(source)::text, 'UTF8')) AS source_key
;

-- name: CountAccountLoginAttempts :one
WITH active AS (
    SELECT expires_at
    FROM public.almanac_login_attempts
    WHERE account_key = sqlc.arg(account_key) AND expires_at > sqlc.arg(now_at)
), total AS (
    SELECT count(*)::bigint AS attempt_count FROM active
)
SELECT total.attempt_count,
       (SELECT active.expires_at FROM active ORDER BY active.expires_at
        OFFSET GREATEST(total.attempt_count - sqlc.arg(limit_count)::bigint, 0) LIMIT 1) AS retry_at
FROM total;

-- name: CountSourceLoginAttempts :one
WITH active AS (
    SELECT expires_at
    FROM public.almanac_login_attempts
    WHERE source_key = sqlc.arg(source_key) AND expires_at > sqlc.arg(now_at)
), total AS (
    SELECT count(*)::bigint AS attempt_count FROM active
)
SELECT total.attempt_count,
       (SELECT active.expires_at FROM active ORDER BY active.expires_at
        OFFSET GREATEST(total.attempt_count - sqlc.arg(limit_count)::bigint, 0) LIMIT 1) AS retry_at
FROM total;

-- name: InsertLoginAttempt :one
INSERT INTO public.almanac_login_attempts (account_key, source_key, started_at, expires_at, state)
VALUES (sqlc.arg(account_key), sqlc.arg(source_key), sqlc.arg(started_at), sqlc.arg(expires_at), 'pending')
RETURNING id;

-- name: PruneExpiredLoginAttempts :exec
WITH expired AS (
    SELECT id
    FROM public.almanac_login_attempts
    WHERE almanac_login_attempts.expires_at <= sqlc.arg(now_at)
    ORDER BY almanac_login_attempts.expires_at, almanac_login_attempts.id
    FOR UPDATE SKIP LOCKED
    LIMIT 100
)
DELETE FROM public.almanac_login_attempts AS attempt
USING expired
WHERE attempt.id = expired.id;

-- name: LoginAttemptKeysByID :one
SELECT account_key, source_key
FROM public.almanac_login_attempts
WHERE id = sqlc.arg(id);

-- name: LockLoginAttemptForFinish :one
SELECT id, account_key, source_key, started_at, expires_at, state
FROM public.almanac_login_attempts
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: DeleteLoginAttempt :exec
DELETE FROM public.almanac_login_attempts WHERE id = sqlc.arg(id);

-- name: FailLoginAttempt :exec
UPDATE public.almanac_login_attempts
SET state = 'failed', expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id);
