-- name: InsertDatasource :one
INSERT INTO public.almanac_datasources (
    id, name, display_name, version, provider, configuration_format, endpoint, tls,
    authentication_mode, authentication_username, password_envelope, password_revision,
    token_envelope, token_revision, polling_interval_seconds, settling_delay_seconds,
    agent_id, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(name), sqlc.arg(display_name), 1, sqlc.arg(provider),
    sqlc.arg(configuration_format), sqlc.arg(endpoint), sqlc.arg(tls),
    sqlc.arg(authentication_mode), sqlc.narg(authentication_username),
    sqlc.narg(password_envelope), sqlc.arg(password_revision), sqlc.narg(token_envelope),
    sqlc.arg(token_revision), sqlc.arg(polling_interval_seconds), sqlc.arg(settling_delay_seconds),
    sqlc.arg(agent_id), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetDatasource :one
SELECT * FROM public.almanac_datasources WHERE id = sqlc.arg(id);

-- name: ListDatasources :many
SELECT * FROM public.almanac_datasources
WHERE (sqlc.narg(agent_id)::uuid IS NULL OR agent_id = sqlc.narg(agent_id))
  AND (created_at, id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: UpdateDatasource :one
UPDATE public.almanac_datasources
SET name = sqlc.arg(name), display_name = sqlc.arg(display_name), version = version + 1,
    provider = sqlc.arg(provider), configuration_format = sqlc.arg(configuration_format),
    endpoint = sqlc.arg(endpoint), tls = sqlc.arg(tls),
    authentication_mode = sqlc.arg(authentication_mode),
    authentication_username = sqlc.narg(authentication_username),
    password_envelope = sqlc.narg(password_envelope), password_revision = sqlc.arg(password_revision),
    token_envelope = sqlc.narg(token_envelope), token_revision = sqlc.arg(token_revision),
    polling_interval_seconds = sqlc.arg(polling_interval_seconds),
    settling_delay_seconds = sqlc.arg(settling_delay_seconds), agent_id = sqlc.arg(agent_id),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: GetDatasourceAgentCapabilities :one
SELECT agent.enabled, runtime.capabilities
FROM public.almanac_agents AS agent
LEFT JOIN public.almanac_agent_runtime_status AS runtime ON runtime.agent_id = agent.id
WHERE agent.id = sqlc.arg(agent_id);

-- name: GetLatestAgentConfigurationRevision :one
SELECT revision FROM public.almanac_agent_configurations
WHERE agent_id = sqlc.arg(agent_id)
ORDER BY revision DESC LIMIT 1 FOR UPDATE;

-- name: InsertAgentConfigurationRevision :exec
INSERT INTO public.almanac_agent_configurations(agent_id, revision, snapshot, created_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(revision), sqlc.arg(snapshot), sqlc.arg(created_at));

-- name: UpdateAgentDesiredConfigurationRevision :execrows
UPDATE public.almanac_agent_runtime_status
SET desired_configuration_revision = sqlc.arg(revision), updated_at = sqlc.arg(updated_at)
WHERE agent_id = sqlc.arg(agent_id);

-- name: FindDatasourceReceipt :one
SELECT digest, result, expires_at FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = 'datasource.create'
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: DeleteExpiredDatasourceReceipt :execrows
DELETE FROM public.almanac_management_receipts
WHERE actor_id = sqlc.arg(actor_id) AND operation = 'datasource.create'
  AND idempotency_key = sqlc.arg(idempotency_key) AND expires_at <= sqlc.arg(now);

-- name: InsertDatasourceReceipt :exec
INSERT INTO public.almanac_management_receipts(actor_id, operation, idempotency_key, digest, result, created_at, expires_at)
VALUES (sqlc.arg(actor_id), 'datasource.create', sqlc.arg(idempotency_key), sqlc.arg(digest),
        sqlc.arg(result), sqlc.arg(created_at), sqlc.arg(expires_at));
