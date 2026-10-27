-- name: EnsureAgentConfiguration :exec
INSERT INTO public.almanac_agent_configurations (agent_id, revision, snapshot, created_at)
VALUES ($1, 1, $2, $3)
ON CONFLICT (agent_id, revision) DO NOTHING;

-- name: GetAgentConfiguration :one
SELECT revision, snapshot, created_at
FROM public.almanac_agent_configurations
WHERE agent_id = $1
ORDER BY revision DESC
LIMIT 1;

-- name: InsertAgentExecutionGrant :exec
INSERT INTO public.almanac_agent_execution_grants (
    lease_id, agent_id, generation, configuration_revision, allowed_measurements,
    valid_from, valid_until, renewal_sequence, request_nonce, retain_until
) VALUES ($1, $2, $3, $4, '[]'::jsonb, $5, $6, $7, $8, $9);

-- name: GetAgentExecutionGrant :one
SELECT lease_id, configuration_revision, allowed_measurements,
       valid_from, valid_until, renewal_sequence, request_nonce
FROM public.almanac_agent_execution_grants
WHERE lease_id = $1 AND agent_id = $2 AND generation = $3
FOR UPDATE;

-- name: GetAgentExecutionGrantBySequence :one
SELECT lease_id, configuration_revision, allowed_measurements,
       valid_from, valid_until, renewal_sequence, request_nonce
FROM public.almanac_agent_execution_grants
WHERE agent_id = $1 AND generation = $2 AND renewal_sequence = $3
FOR UPDATE;

-- name: SetAgentCurrentExecutionLease :execrows
UPDATE public.almanac_agent_ownership
SET current_lease_id = $3
WHERE agent_id = $1 AND generation = $2;

-- name: DeleteExpiredAgentExecutionGrants :exec
DELETE FROM public.almanac_agent_execution_grants AS grant_row
WHERE grant_row.agent_id = $1
  AND grant_row.retain_until < $2
  AND grant_row.lease_id <> COALESCE((
      SELECT owner.current_lease_id FROM public.almanac_agent_ownership AS owner
      WHERE owner.agent_id = grant_row.agent_id AND owner.generation = grant_row.generation
  ), '');

-- name: UpsertAgentRuntimeHello :exec
INSERT INTO public.almanac_agent_runtime_status (
    agent_id, generation, protocol, software_version, capabilities,
    desired_configuration_revision, applied_configuration_revision,
    scheduler_health, collection_progress, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 0, 'starting', 'not_started', $7)
ON CONFLICT (agent_id) DO UPDATE SET
    generation = EXCLUDED.generation,
    protocol = EXCLUDED.protocol,
    software_version = EXCLUDED.software_version,
    capabilities = EXCLUDED.capabilities,
    last_heartbeat_at = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.last_heartbeat_at ELSE NULL END,
    desired_configuration_revision = EXCLUDED.desired_configuration_revision,
    applied_configuration_revision = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.applied_configuration_revision ELSE 0 END,
    scheduler_health = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.scheduler_health ELSE 'starting' END,
    collection_progress = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.collection_progress ELSE 'not_started' END,
    last_attempt_at = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.last_attempt_at ELSE NULL END,
    last_successful_query_at = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.last_successful_query_at ELSE NULL END,
    next_expected_execution_at = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.next_expected_execution_at ELSE NULL END,
    configuration_failure_code = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.configuration_failure_code ELSE NULL END,
    configuration_failure_revision = CASE
        WHEN public.almanac_agent_runtime_status.generation = EXCLUDED.generation
        THEN public.almanac_agent_runtime_status.configuration_failure_revision ELSE NULL END,
    updated_at = EXCLUDED.updated_at;

-- name: UpsertAgentIncompatibleAttempt :exec
INSERT INTO public.almanac_agent_incompatible_attempts (agent_id, attempted_at)
VALUES ($1, $2)
ON CONFLICT (agent_id) DO UPDATE SET attempted_at = EXCLUDED.attempted_at;

-- name: DeleteAgentIncompatibleAttempt :exec
DELETE FROM public.almanac_agent_incompatible_attempts
WHERE agent_id = $1;

-- name: GetAgentIncompatibleAttempt :one
SELECT attempted_at
FROM public.almanac_agent_incompatible_attempts
WHERE agent_id = $1;

-- name: UpdateAgentRuntimeHeartbeat :execrows
UPDATE public.almanac_agent_runtime_status AS status
SET last_heartbeat_at = sqlc.arg(received_at),
    applied_configuration_revision = sqlc.arg(applied_configuration_revision),
    scheduler_health = CASE
        WHEN status.configuration_failure_code IS NOT NULL
          AND sqlc.arg(applied_configuration_revision) < status.desired_configuration_revision
        THEN 'degraded' ELSE sqlc.arg(scheduler_health) END,
    collection_progress = CASE
        WHEN status.configuration_failure_code IS NOT NULL
          AND sqlc.arg(applied_configuration_revision) < status.desired_configuration_revision
        THEN 'blocked' ELSE sqlc.arg(collection_progress) END,
    configuration_failure_code = CASE
        WHEN sqlc.arg(applied_configuration_revision) >= status.desired_configuration_revision
        THEN NULL ELSE status.configuration_failure_code END,
    configuration_failure_revision = CASE
        WHEN sqlc.arg(applied_configuration_revision) >= status.desired_configuration_revision
        THEN NULL ELSE status.configuration_failure_revision END,
    last_attempt_at = sqlc.narg(last_attempt_at),
    last_successful_query_at = sqlc.narg(last_successful_query_at),
    next_expected_execution_at = sqlc.narg(next_expected_execution_at),
    updated_at = sqlc.arg(received_at)
WHERE status.agent_id = sqlc.arg(agent_id) AND status.generation = sqlc.arg(generation)
  AND status.desired_configuration_revision >= sqlc.arg(applied_configuration_revision)
  AND EXISTS (
      SELECT 1 FROM public.almanac_agent_ownership AS owner
      WHERE owner.agent_id = status.agent_id AND owner.generation = status.generation
        AND owner.expires_at > sqlc.arg(received_at)
  );

-- name: UpdateAgentAppliedConfiguration :execrows
UPDATE public.almanac_agent_runtime_status AS status
SET applied_configuration_revision = sqlc.arg(applied_configuration_revision),
    configuration_failure_code = CASE
        WHEN sqlc.arg(applied_configuration_revision) < status.desired_configuration_revision
        THEN status.configuration_failure_code ELSE NULL END,
    configuration_failure_revision = CASE
        WHEN sqlc.arg(applied_configuration_revision) < status.desired_configuration_revision
        THEN status.configuration_failure_revision ELSE NULL END,
    updated_at = sqlc.arg(received_at)
WHERE status.agent_id = sqlc.arg(agent_id) AND status.generation = sqlc.arg(generation)
  AND sqlc.arg(applied_configuration_revision) <= status.desired_configuration_revision
  AND EXISTS (
      SELECT 1 FROM public.almanac_agent_ownership AS owner
      WHERE owner.agent_id = status.agent_id AND owner.generation = status.generation
        AND owner.expires_at > sqlc.arg(received_at)
  );

-- name: RecordAgentConfigurationFailed :execrows
UPDATE public.almanac_agent_runtime_status AS status
SET configuration_failure_code = sqlc.arg(configuration_failure_code),
    configuration_failure_revision = sqlc.arg(failed_configuration_revision),
    scheduler_health = 'degraded',
    collection_progress = 'blocked',
    updated_at = sqlc.arg(received_at)
WHERE status.agent_id = sqlc.arg(agent_id) AND status.generation = sqlc.arg(generation)
  AND status.desired_configuration_revision = sqlc.arg(failed_configuration_revision)
  AND status.applied_configuration_revision < sqlc.arg(failed_configuration_revision)
  AND EXISTS (
      SELECT 1 FROM public.almanac_agent_ownership AS owner
      WHERE owner.agent_id = status.agent_id AND owner.generation = status.generation
        AND owner.expires_at > sqlc.arg(received_at)
  );

-- name: GetAgentRuntimeStatus :one
SELECT generation, protocol, software_version, capabilities, last_heartbeat_at,
       desired_configuration_revision, applied_configuration_revision,
       scheduler_health, collection_progress, configuration_failure_code, configuration_failure_revision, last_attempt_at,
       last_successful_query_at, next_expected_execution_at, updated_at
FROM public.almanac_agent_runtime_status
WHERE agent_id = $1;
