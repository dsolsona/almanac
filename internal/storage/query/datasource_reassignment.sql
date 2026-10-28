-- name: GetDatasourceReassignment :one
SELECT * FROM public.almanac_datasource_reassignments WHERE datasource_id = $1;

-- name: LockDatasourceReassignment :one
SELECT * FROM public.almanac_datasource_reassignments WHERE datasource_id = $1 FOR UPDATE;

-- name: InsertDatasourceReassignment :exec
INSERT INTO public.almanac_datasource_reassignments
(datasource_id, source_agent_id, target_agent_id, requested_at, authority_expires_at, removal_revision, source_config_version, uses_v2_removal, retry_at, state, actor_id, request_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $4, 'waiting_for_previous_agent', $9, $10);

-- name: MarkDatasourceReassignmentDrained :execrows
UPDATE public.almanac_datasource_reassignments SET drained_at = $3
WHERE source_agent_id = $1 AND removal_revision = $2 AND uses_v2_removal AND drained_at IS NULL;

-- name: DeleteDatasourceReassignment :execrows
DELETE FROM public.almanac_datasource_reassignments WHERE datasource_id = $1;

-- name: SetDatasourceReassignmentUnavailable :execrows
UPDATE public.almanac_datasource_reassignments SET state = 'replacement_unavailable', retry_at = $2
WHERE datasource_id = $1;

-- name: ListReadyDatasourceReassignments :many
SELECT datasource_id, source_agent_id, target_agent_id FROM public.almanac_datasource_reassignments
WHERE (drained_at IS NOT NULL OR authority_expires_at <= $1) AND retry_at <= $1 ORDER BY retry_at, requested_at, datasource_id LIMIT 50;

-- name: LatestDatasourceGrantExpiry :one
SELECT COALESCE(max(grant_row.valid_until), sqlc.arg(now)::timestamptz)::timestamptz AS expires_at
FROM public.almanac_agent_execution_grants AS grant_row
WHERE grant_row.agent_id = sqlc.arg(agent_id)
  AND grant_row.valid_until > sqlc.arg(now)::timestamptz
  AND EXISTS (
    SELECT 1 FROM jsonb_array_elements(grant_row.allowed_measurements) AS allowed
    JOIN public.almanac_sli_measurements AS measurement ON measurement.sli_id::text = allowed->>'sli_id'
      AND measurement.revision::text = allowed->>'measurement_revision'
    WHERE measurement.datasource_id = sqlc.arg(datasource_id)
  );

-- name: BumpDatasourceVersion :one
UPDATE public.almanac_datasources SET version = version + 1, updated_at = $2
WHERE id = $1 AND version < 9223372036854775807 RETURNING *;

-- name: ActivateDatasourceAgent :one
UPDATE public.almanac_datasources SET agent_id = $2, version = version + 1, updated_at = $3
WHERE id = $1 AND version < 9223372036854775807 RETURNING *;

-- name: ListAgentDatasourceDependencies :many
SELECT d.* FROM public.almanac_datasources d
WHERE (d.agent_id = sqlc.arg(agent_id) OR EXISTS (SELECT 1 FROM public.almanac_datasource_reassignments r WHERE r.datasource_id = d.id AND (r.source_agent_id = sqlc.arg(agent_id) OR r.target_agent_id = sqlc.arg(agent_id))))
  AND (d.created_at, d.id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY d.created_at, d.id LIMIT sqlc.arg(row_limit);

-- name: AgentHasPendingReassignments :one
SELECT EXISTS (SELECT 1 FROM public.almanac_datasource_reassignments WHERE source_agent_id = $1 UNION ALL SELECT 1 FROM public.almanac_agent_handoff_configurations WHERE agent_id = $1);

-- name: AgentHasUnsettledCurrentLease :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_agent_ownership owner
    JOIN public.almanac_agent_execution_grants grant_row ON grant_row.lease_id = owner.current_lease_id
    WHERE owner.agent_id = sqlc.arg(agent_id)
      AND owner.generation = (SELECT max(generation) FROM public.almanac_agent_ownership WHERE agent_id = sqlc.arg(agent_id))
      AND owner.expires_at > sqlc.arg(now)::timestamptz
      AND grant_row.configuration_revision <> sqlc.arg(configuration_revision)::bigint
);

-- name: AgentHasLiveOwnership :one
SELECT EXISTS (SELECT 1 FROM public.almanac_agent_ownership
               WHERE agent_id = sqlc.arg(agent_id) AND expires_at > sqlc.arg(now)::timestamptz);

-- name: UpdateSourceRemovalRevisions :exec
UPDATE public.almanac_datasource_reassignments SET removal_revision = $2
WHERE source_agent_id = $1 AND uses_v2_removal;

-- name: InsertSliOwnershipEpoch :exec
INSERT INTO public.almanac_sli_ownership_epochs(sli_id, measurement_revision, agent_id, starts_at, ends_at)
VALUES ($1, $2, $3, $4, $5);

-- name: CloseSliOwnershipEpoch :execrows
UPDATE public.almanac_sli_ownership_epochs SET ends_at = $3
WHERE sli_id = $1 AND measurement_revision = $2 AND ends_at IS NULL;

-- name: ListDatasourceSliMeasurements :many
SELECT m.sli_id, m.revision, m.agent_id, m.effective_from, m.activated_at
FROM public.almanac_sli_measurements m JOIN public.almanac_slis s ON s.id = m.sli_id AND s.measurement_revision = m.revision
WHERE m.datasource_id = $1 ORDER BY m.sli_id FOR UPDATE OF m, s;

-- name: SetAgentHandoffConfiguration :exec
INSERT INTO public.almanac_agent_handoff_configurations(agent_id, revision) VALUES ($1, $2)
ON CONFLICT (agent_id) DO UPDATE SET revision = EXCLUDED.revision;

-- name: GetAgentHandoffConfiguration :one
SELECT revision FROM public.almanac_agent_handoff_configurations WHERE agent_id = $1;

-- name: DeleteAcknowledgedAgentHandoffConfiguration :execrows
DELETE FROM public.almanac_agent_handoff_configurations WHERE agent_id = $1 AND revision = $2;

-- name: DeleteExpiredSourceHandoffConfiguration :exec
DELETE FROM public.almanac_agent_handoff_configurations marker
WHERE marker.agent_id = $1
  AND NOT EXISTS (SELECT 1 FROM public.almanac_datasource_reassignments pending WHERE pending.source_agent_id = marker.agent_id);

-- name: GetCurrentSliOwnershipEpoch :one
SELECT * FROM public.almanac_sli_ownership_epochs WHERE sli_id = $1 AND measurement_revision = $2 AND ends_at IS NULL FOR UPDATE;

-- name: SetDatasourceEpochConfigurationRevision :exec
UPDATE public.almanac_sli_ownership_epochs e SET configuration_revision = $2
FROM public.almanac_sli_measurements m JOIN public.almanac_slis s ON s.id = m.sli_id AND s.measurement_revision = m.revision
WHERE e.sli_id = m.sli_id AND e.measurement_revision = m.revision AND m.datasource_id = $1 AND e.ends_at IS NULL;
