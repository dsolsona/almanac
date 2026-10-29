-- name: FindSliCreateReceipt :one
SELECT request_digest, result, expires_at
FROM public.almanac_sli_create_receipts
WHERE actor_id = $1 AND idempotency_key = $2
FOR UPDATE;

-- name: DeleteExpiredSliCreateReceipt :exec
DELETE FROM public.almanac_sli_create_receipts
WHERE actor_id = $1 AND idempotency_key = $2 AND expires_at <= $3;

-- name: InsertSliCreateReceipt :exec
INSERT INTO public.almanac_sli_create_receipts(actor_id, idempotency_key, request_digest, result, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: InsertSli :exec
INSERT INTO public.almanac_slis(id, name, display_name, version, measurement_revision, created_at, updated_at)
VALUES ($1, $2, $3, 1, 1, $4, $4);

-- name: InsertSliMeasurement :exec
INSERT INTO public.almanac_sli_measurements(
    sli_id, revision, datasource_id, agent_id, measurement_kind, component_mode,
    good_query, total_query, effective_from, created_at
) VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: HasSLIsForDatasource :one
SELECT EXISTS (
    SELECT 1
    FROM public.almanac_sli_measurements AS measurement
    JOIN public.almanac_slis AS sli
      ON sli.id = measurement.sli_id AND sli.measurement_revision = measurement.revision
    WHERE measurement.datasource_id = $1
);

-- name: GetSli :one
SELECT sli.id, sli.name, sli.display_name, sli.version, sli.measurement_revision,
       measurement.datasource_id, datasource.version AS datasource_version,
       measurement.measurement_kind, measurement.component_mode,
       measurement.good_query, measurement.total_query, measurement.effective_from, measurement.activated_at,
       datasource.agent_id, datasource.polling_interval_seconds,
       datasource.settling_delay_seconds, sli.created_at, sli.updated_at,
       agent.enabled AS agent_enabled,
       runtime.last_heartbeat_at, runtime.updated_at AS runtime_updated_at,
       runtime.desired_configuration_revision, runtime.applied_configuration_revision,
       runtime.scheduler_health, runtime.collection_progress,
       runtime.configuration_failure_code, incompatible.attempted_at AS incompatible_attempted_at,
       runtime.last_attempt_at,
       runtime.last_successful_query_at, runtime.next_expected_execution_at
FROM public.almanac_slis AS sli
JOIN public.almanac_sli_measurements AS measurement
  ON measurement.sli_id = sli.id AND measurement.revision = sli.measurement_revision
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
LEFT JOIN public.almanac_agent_runtime_status AS runtime ON runtime.agent_id = datasource.agent_id
JOIN public.almanac_agents AS agent ON agent.id = datasource.agent_id
LEFT JOIN public.almanac_agent_incompatible_attempts AS incompatible ON incompatible.agent_id = datasource.agent_id
WHERE sli.id = $1;

-- name: ListSlis :many
WITH page AS MATERIALIZED (
    SELECT sli.id, sli.created_at
    FROM public.almanac_slis AS sli
    JOIN public.almanac_sli_measurements AS measurement
      ON measurement.sli_id = sli.id AND measurement.revision = sli.measurement_revision
    JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
    WHERE (sqlc.arg(query)::text = '' OR position(sqlc.arg(query)::text IN lower(sli.name)) > 0
           OR position(sqlc.arg(query)::text IN lower(sli.display_name)) > 0
           OR position(sqlc.arg(query)::text IN lower(datasource.display_name)) > 0)
      AND (sqlc.narg(datasource_filter)::uuid IS NULL OR datasource.id = sqlc.narg(datasource_filter)::uuid)
      AND (sli.created_at, sli.id) > (sqlc.arg(created_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
    ORDER BY sli.created_at, sli.id
    LIMIT sqlc.arg(limit_count)::integer
)
SELECT sli.id, sli.name, sli.display_name, sli.version, sli.measurement_revision,
       measurement.datasource_id, datasource.version AS datasource_version,
       datasource.display_name AS datasource_display_name,
       measurement.measurement_kind, measurement.component_mode,
       measurement.good_query, measurement.total_query, measurement.effective_from, measurement.activated_at,
       datasource.agent_id, datasource.polling_interval_seconds,
       datasource.settling_delay_seconds, sli.created_at, sli.updated_at,
       agent.enabled AS agent_enabled,
       runtime.last_heartbeat_at, runtime.updated_at AS runtime_updated_at,
       runtime.desired_configuration_revision, runtime.applied_configuration_revision,
       runtime.scheduler_health, runtime.collection_progress,
       runtime.configuration_failure_code, incompatible.attempted_at AS incompatible_attempted_at,
       runtime.last_attempt_at,
       runtime.last_successful_query_at, runtime.next_expected_execution_at,
       latest.observation_id AS latest_observation_id,
       COALESCE(latest.measurement_revision, 0)::bigint AS latest_measurement_revision,
       latest.interval_start AS latest_interval_start,
       latest.interval_end AS latest_interval_end,
       COALESCE(latest.outcome, '')::text AS latest_outcome,
       latest.good_count AS latest_good_count,
       latest.total_count AS latest_total_count,
       latest.error_code AS latest_error_code,
       latest.missing_components AS latest_missing_components,
       latest.recorded_at AS latest_recorded_at,
       accepted.last_observation_at,
       recent.window_start AS recent_measurement_window_start,
       recent.window_end AS recent_measurement_window_end,
       recent.items::text AS recent_measurements,
       linked.items::text AS linked_slos,
       linked.total_count AS linked_slo_count
FROM page
JOIN public.almanac_slis AS sli ON sli.id = page.id
JOIN public.almanac_sli_measurements AS measurement
  ON measurement.sli_id = sli.id AND measurement.revision = sli.measurement_revision
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
LEFT JOIN public.almanac_agent_runtime_status AS runtime ON runtime.agent_id = datasource.agent_id
JOIN public.almanac_agents AS agent ON agent.id = datasource.agent_id
LEFT JOIN public.almanac_agent_incompatible_attempts AS incompatible ON incompatible.agent_id = datasource.agent_id
LEFT JOIN LATERAL (
    SELECT coverage.recorded_at::timestamptz AS last_observation_at
    FROM public.almanac_sli_coverage AS coverage
    WHERE coverage.sli_id = sli.id
      AND coverage.interval_start >= sqlc.arg(retention_cutoff)::timestamptz
      AND coverage.outcome IN ('observed', 'complete_empty')
    ORDER BY coverage.recorded_at DESC
    LIMIT 1
) AS accepted ON true
LEFT JOIN LATERAL (
    SELECT coverage.observation_id, coverage.measurement_revision, coverage.interval_start,
           coverage.interval_end, coverage.outcome, COALESCE(obs.good_count::text, '') AS good_count,
           COALESCE(obs.total_count::text, '') AS total_count, coverage.error_code,
           coverage.missing_components, coverage.recorded_at
    FROM public.almanac_sli_coverage AS coverage
    LEFT JOIN public.almanac_sli_observation_values AS obs
      ON obs.interval_start = coverage.interval_start AND obs.sli_id = coverage.sli_id
     AND obs.measurement_revision = coverage.measurement_revision
    WHERE coverage.sli_id = sli.id AND coverage.interval_start >= sqlc.arg(retention_cutoff)::timestamptz
    UNION ALL
    SELECT attempt.observation_id, attempt.measurement_revision, attempt.interval_start,
           attempt.interval_end, attempt.outcome, '' AS good_count, '' AS total_count,
           attempt.error_code, attempt.missing_components, attempt.recorded_at
    FROM public.almanac_sli_execution_attempts AS attempt
    WHERE attempt.sli_id = sli.id AND attempt.interval_start >= sqlc.arg(retention_cutoff)::timestamptz
      AND NOT EXISTS (SELECT 1 FROM public.almanac_sli_coverage c WHERE c.sli_id = attempt.sli_id
        AND c.measurement_revision = attempt.measurement_revision AND c.interval_start = attempt.interval_start)
      AND NOT EXISTS (SELECT 1 FROM public.almanac_sli_execution_attempts newer WHERE newer.sli_id = attempt.sli_id
        AND newer.measurement_revision = attempt.measurement_revision AND newer.interval_start = attempt.interval_start
        AND (newer.recorded_at, newer.execution_id) > (attempt.recorded_at, attempt.execution_id))
    ORDER BY interval_start DESC, recorded_at DESC
    LIMIT 1
) AS latest ON true
LEFT JOIN LATERAL (
    SELECT min(sample.interval_start)::timestamptz AS window_start,
           max(sample.interval_end)::timestamptz AS window_end,
           COALESCE(jsonb_agg(jsonb_build_object(
               'interval_start', sample.interval_start,
               'interval_end', sample.interval_end,
               'state', sample.state,
               'good_count', sample.good_count,
               'total_count', sample.total_count
           ) ORDER BY sample.interval_start), '[]'::jsonb) AS items
    FROM (
        SELECT slot.interval_start,
               slot.interval_start + make_interval(secs => datasource.polling_interval_seconds) AS interval_end,
               CASE
                   WHEN evidence.outcome = 'observed' AND evidence.total_count::numeric = 0 THEN 'zero_traffic'
                   WHEN evidence.outcome = 'observed' THEN 'observed'
                   WHEN evidence.outcome = 'complete_empty' THEN 'known_empty'
                   WHEN evidence.outcome = 'incomplete' THEN 'partial'
                   WHEN evidence.outcome = 'failed' THEN 'failed'
                   ELSE 'missing'
               END::text AS state,
               evidence.good_count,
               evidence.total_count
        FROM generate_series(
            GREATEST(
                GREATEST(
                    to_timestamp(ceil(extract(epoch FROM sqlc.arg(retention_cutoff)::timestamptz) / datasource.polling_interval_seconds) * datasource.polling_interval_seconds),
                    measurement.effective_from
                ),
                to_timestamp(floor(extract(epoch FROM (sqlc.arg(list_now)::timestamptz - make_interval(secs => datasource.settling_delay_seconds))) / datasource.polling_interval_seconds) * datasource.polling_interval_seconds)
                    - make_interval(secs => 12 * datasource.polling_interval_seconds)
            ),
            to_timestamp(floor(extract(epoch FROM (sqlc.arg(list_now)::timestamptz - make_interval(secs => datasource.settling_delay_seconds))) / datasource.polling_interval_seconds) * datasource.polling_interval_seconds)
                - make_interval(secs => datasource.polling_interval_seconds),
            make_interval(secs => datasource.polling_interval_seconds)
        ) AS slot(interval_start)
        LEFT JOIN LATERAL (
            SELECT picked.outcome, picked.good_count, picked.total_count
            FROM (
                SELECT coverage.outcome,
                       obs.good_count::text AS good_count,
                       obs.total_count::text AS total_count,
                       0 AS precedence,
                       coverage.recorded_at,
                       coverage.observation_id AS tie_id
                FROM public.almanac_sli_coverage AS coverage
                LEFT JOIN public.almanac_sli_observation_values AS obs
                  ON obs.interval_start = coverage.interval_start
                 AND obs.sli_id = coverage.sli_id
                 AND obs.measurement_revision = coverage.measurement_revision
                WHERE coverage.sli_id = sli.id
                  AND coverage.measurement_revision = measurement.revision
                  AND coverage.interval_start = slot.interval_start
                UNION ALL
                SELECT attempt.outcome, NULL::text, NULL::text, 1 AS precedence,
                       attempt.recorded_at, attempt.execution_id AS tie_id
                FROM public.almanac_sli_execution_attempts AS attempt
                WHERE attempt.sli_id = sli.id
                  AND attempt.measurement_revision = measurement.revision
                  AND attempt.interval_start = slot.interval_start
                  AND NOT EXISTS (
                      SELECT 1 FROM public.almanac_sli_coverage AS covered
                      WHERE covered.sli_id = attempt.sli_id
                        AND covered.measurement_revision = attempt.measurement_revision
                        AND covered.interval_start = attempt.interval_start
                  )
            ) AS picked
            ORDER BY picked.precedence, picked.recorded_at DESC, picked.tie_id DESC
            LIMIT 1
        ) AS evidence ON true
        WHERE measurement.activated_at IS NOT NULL
    ) AS sample
) AS recent ON true
LEFT JOIN LATERAL (
    SELECT COALESCE((
               SELECT jsonb_agg(jsonb_build_object('id', capped.id, 'name', capped.name, 'display_name', capped.display_name)
                                ORDER BY capped.created_at, capped.id)
               FROM (
                   SELECT slo.id, slo.name, slo.display_name, slo.created_at
                   FROM public.almanac_slo_definitions AS definition
                   JOIN public.almanac_slos AS slo
                     ON slo.id = definition.slo_id AND slo.definition_revision = definition.revision
                   WHERE definition.sli_id = sli.id
                   ORDER BY slo.created_at, slo.id
                   LIMIT 5
               ) AS capped
           ), '[]'::jsonb) AS items,
           (SELECT count(*)::integer
            FROM public.almanac_slo_definitions AS definition
            JOIN public.almanac_slos AS slo
              ON slo.id = definition.slo_id AND slo.definition_revision = definition.revision
            WHERE definition.sli_id = sli.id) AS total_count
) AS linked ON true
ORDER BY page.created_at, page.id;

-- name: UpdateSliDisplayName :one
UPDATE public.almanac_slis
SET display_name = $1, version = version + 1, updated_at = $2
WHERE id = $3 AND version = $4
RETURNING id, name, display_name, version, measurement_revision, created_at, updated_at;

-- name: LockSliMetadata :one
SELECT version, display_name FROM public.almanac_slis WHERE id = $1 FOR UPDATE;

-- name: ListSliConfigurationForAgent :many
SELECT sli.id AS sli_id, sli.measurement_revision, measurement.datasource_id,
       COALESCE(pending.source_config_version, datasource.version)::bigint AS datasource_version, measurement.measurement_kind,
       measurement.component_mode, measurement.good_query, measurement.total_query,
       GREATEST(measurement.effective_from, COALESCE(epoch.starts_at, measurement.effective_from))::timestamptz AS effective_from
FROM public.almanac_slis AS sli
JOIN public.almanac_sli_measurements AS measurement
  ON measurement.sli_id = sli.id AND measurement.revision = sli.measurement_revision
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
LEFT JOIN public.almanac_datasource_reassignments AS pending ON pending.datasource_id = datasource.id AND NOT pending.uses_v2_removal
LEFT JOIN public.almanac_sli_ownership_epochs AS epoch ON epoch.sli_id = measurement.sli_id AND epoch.measurement_revision = measurement.revision AND epoch.ends_at IS NULL
WHERE datasource.agent_id = $1
  AND NOT EXISTS (SELECT 1 FROM public.almanac_datasource_reassignments r WHERE r.datasource_id = datasource.id AND r.uses_v2_removal)
ORDER BY sli.id;

-- name: ListAuthorizedMeasurementGrantsForAgent :many
SELECT sli.id AS sli_id, sli.measurement_revision, measurement.last_attempted_interval_end
FROM public.almanac_slis AS sli
JOIN public.almanac_sli_measurements AS measurement
  ON measurement.sli_id = sli.id AND measurement.revision = sli.measurement_revision
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
WHERE datasource.agent_id = sqlc.arg(agent_id)
  AND NOT EXISTS (SELECT 1 FROM public.almanac_datasource_reassignments r WHERE r.datasource_id = datasource.id)
  AND NOT EXISTS (SELECT 1 FROM public.almanac_sli_ownership_epochs e WHERE e.sli_id = measurement.sli_id AND e.measurement_revision = measurement.revision AND e.ends_at IS NULL AND e.configuration_revision > sqlc.arg(configuration_revision))
  AND measurement.activated_at IS NOT NULL
ORDER BY sli.id;

-- name: ListPendingSlisForAgent :many
SELECT measurement.sli_id, measurement.revision, measurement.effective_from,
       datasource.polling_interval_seconds
FROM public.almanac_sli_measurements AS measurement
JOIN public.almanac_slis AS sli
  ON sli.id = measurement.sli_id AND sli.measurement_revision = measurement.revision
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
WHERE datasource.agent_id = $1 AND measurement.activated_at IS NULL
ORDER BY measurement.sli_id
FOR UPDATE OF measurement, sli;

-- name: UpdateSliEffectiveFrom :execrows
UPDATE public.almanac_sli_measurements
SET effective_from = $1
WHERE sli_id = $2 AND revision = $3 AND effective_from = $4 AND activated_at IS NULL;

-- name: ActivateSliMeasurement :execrows
UPDATE public.almanac_sli_measurements
SET activated_at = $4
WHERE sli_id = $1 AND revision = $2 AND effective_from = $3
  AND activated_at IS NULL AND (revision = 1 OR effective_from > $4);

-- name: GetAgentExecutionGrantByLease :one
SELECT lease_id, agent_id, generation, configuration_revision, allowed_measurements,
       valid_from, valid_until, retain_until
FROM public.almanac_agent_execution_grants
WHERE lease_id = $1
FOR UPDATE;

-- name: LockSliMeasurementForObservation :one
SELECT measurement.sli_id, measurement.revision, measurement.datasource_id,
       COALESCE((SELECT epoch.agent_id FROM public.almanac_sli_ownership_epochs epoch
           WHERE epoch.sli_id = measurement.sli_id AND epoch.measurement_revision = measurement.revision
             AND epoch.starts_at <= sqlc.arg(interval_start)::timestamptz
             AND (epoch.ends_at IS NULL OR epoch.ends_at > sqlc.arg(interval_start)::timestamptz)),
         CASE WHEN NOT EXISTS (SELECT 1 FROM public.almanac_sli_ownership_epochs epoch WHERE epoch.sli_id = measurement.sli_id AND epoch.measurement_revision = measurement.revision) THEN measurement.agent_id END)::uuid AS interval_agent_id,
       measurement.measurement_kind, measurement.component_mode,
       measurement.effective_from, datasource.agent_id AS current_agent_id,
       datasource.polling_interval_seconds, datasource.settling_delay_seconds,
       sli.measurement_revision AS current_revision
FROM public.almanac_sli_measurements AS measurement
JOIN public.almanac_slis AS sli ON sli.id = measurement.sli_id
JOIN public.almanac_datasources AS datasource ON datasource.id = measurement.datasource_id
WHERE measurement.sli_id = $1 AND measurement.revision = $2
FOR UPDATE OF measurement, sli, datasource;

-- name: AdvanceSliLastAttemptedInterval :exec
UPDATE public.almanac_sli_measurements
SET last_attempted_interval_end = GREATEST(last_attempted_interval_end, sqlc.arg(interval_end)::timestamptz)
WHERE sli_id = sqlc.arg(sli_id) AND revision = sqlc.arg(revision);

-- name: FindSliDeliveryReceipt :one
SELECT payload_digest, interval_start, sli_id, measurement_revision,
       delivery_status, observation_id, replay_until, agent_id,
       session_generation, lease_id, configuration_revision, created_at
FROM public.almanac_sli_delivery_receipts
WHERE agent_id = $1 AND delivery_id = $2
FOR UPDATE;

-- name: GetSliCoverageByCanonicalInterval :one
SELECT result_digest, observation_id, outcome, recorded_at
FROM public.almanac_sli_coverage
WHERE interval_start = $1 AND sli_id = $2 AND measurement_revision = $3
FOR UPDATE;

-- name: InsertSliCoverage :exec
INSERT INTO public.almanac_sli_coverage(
    interval_start, sli_id, measurement_revision, interval_end, outcome,
    result_digest, error_code, missing_components, execution_id, delivery_id,
    observation_id, agent_id, session_generation, configuration_revision,
    lease_id, agent_created_at, recorded_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: InsertSliExecutionAttempt :exec
INSERT INTO public.almanac_sli_execution_attempts(
    agent_id, execution_id, observation_id, sli_id, measurement_revision,
    interval_start, interval_end, outcome, error_code, missing_components, lease_id, recorded_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: HasSliExecutionAttempt :one
SELECT EXISTS (SELECT 1 FROM public.almanac_sli_execution_attempts WHERE agent_id = $1 AND execution_id = $2);

-- name: InsertSliObservationValue :exec
INSERT INTO public.almanac_sli_observation_values(
    interval_start, sli_id, measurement_revision, good_count, total_count
) VALUES ($1, $2, $3, sqlc.arg(good_count)::text::numeric(38,12), sqlc.arg(total_count)::text::numeric(38,12));

-- name: InsertSliDirtyInterval :exec
INSERT INTO public.almanac_sli_dirty_intervals(sli_id, measurement_revision, interval_start, interval_end, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (sli_id, measurement_revision, interval_start) DO NOTHING;

-- name: InsertSliDeliveryReceipt :exec
INSERT INTO public.almanac_sli_delivery_receipts(
    delivery_id, payload_digest, sli_id, measurement_revision, interval_start,
    delivery_status, observation_id, replay_until, agent_id, session_generation,
    lease_id, configuration_revision, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListSliObservations :many
SELECT * FROM (
SELECT coverage.observation_id, coverage.measurement_revision,
       coverage.interval_start, coverage.interval_end, coverage.outcome,
       COALESCE(obs.good_count::text, '') AS good_count, COALESCE(obs.total_count::text, '') AS total_count,
       coverage.error_code, coverage.missing_components, coverage.recorded_at
FROM public.almanac_sli_coverage AS coverage
LEFT JOIN public.almanac_sli_observation_values AS obs
  ON obs.interval_start = coverage.interval_start AND obs.sli_id = coverage.sli_id
 AND obs.measurement_revision = coverage.measurement_revision
WHERE coverage.sli_id = $1 AND
      coverage.interval_start >= sqlc.arg(retention_cutoff)::timestamptz AND
      coverage.interval_start >= sqlc.arg(range_start)::timestamptz AND
      ($2::timestamptz IS NULL OR coverage.interval_start < $2)
UNION ALL
SELECT attempt.observation_id, attempt.measurement_revision, attempt.interval_start,
       attempt.interval_end, attempt.outcome, '' AS good_count, '' AS total_count,
       attempt.error_code, attempt.missing_components, attempt.recorded_at
FROM public.almanac_sli_execution_attempts AS attempt
WHERE attempt.sli_id = $1 AND attempt.interval_start >= sqlc.arg(retention_cutoff)::timestamptz
  AND attempt.interval_start >= sqlc.arg(range_start)::timestamptz
  AND ($2::timestamptz IS NULL OR attempt.interval_start < $2)
  AND NOT EXISTS (SELECT 1 FROM public.almanac_sli_coverage c WHERE c.sli_id = attempt.sli_id
    AND c.measurement_revision = attempt.measurement_revision AND c.interval_start = attempt.interval_start)
  AND NOT EXISTS (SELECT 1 FROM public.almanac_sli_execution_attempts newer WHERE newer.sli_id = attempt.sli_id
    AND newer.measurement_revision = attempt.measurement_revision AND newer.interval_start = attempt.interval_start
    AND (newer.recorded_at, newer.execution_id) > (attempt.recorded_at, attempt.execution_id))
) AS history
ORDER BY interval_start DESC, recorded_at DESC
LIMIT $3;

-- name: LatestSliAcceptedAt :one
SELECT max(recorded_at)::timestamptz FROM public.almanac_sli_coverage
WHERE sli_id = $1 AND interval_start >= $2 AND outcome IN ('observed', 'complete_empty');
