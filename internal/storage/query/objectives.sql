-- name: GetService :one
SELECT id, name, display_name, version, created_at, updated_at FROM public.almanac_services WHERE id = $1;

-- name: ListServices :many
SELECT id, name, display_name, version, created_at, updated_at FROM public.almanac_services
WHERE (created_at, id) > ($1, $2::uuid) ORDER BY created_at, id LIMIT $3;

-- name: InsertService :one
INSERT INTO public.almanac_services(id, name, display_name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4) RETURNING *;

-- name: UpdateServiceMetadata :one
UPDATE public.almanac_services SET display_name = $1, version = version + 1, updated_at = $2
WHERE id = $3 AND version = $4 RETURNING *;

-- name: GetSlo :one
SELECT s.id, s.name, s.display_name, s.version, s.definition_revision, s.created_at, s.updated_at,
       d.service_id, service.display_name AS service_display_name, d.sli_id, sli.display_name AS sli_display_name,
       d.measurement_revision, d.target::text AS target, d.rolling_window_seconds, d.cadence_seconds,
       d.activated_at, d.evaluation_valid_from, d.historical_requested_by, d.historical_requested_at,
       measurement.effective_from AS sli_valid_from
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_services service ON service.id=d.service_id
JOIN public.almanac_slis sli ON sli.id=d.sli_id
JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=d.sli_id AND measurement.revision=d.measurement_revision
WHERE s.id=$1;

-- name: ListSlos :many
SELECT s.id, s.name, s.display_name, s.version, s.definition_revision, s.created_at, s.updated_at,
       d.service_id, service.display_name AS service_display_name, d.sli_id, sli.display_name AS sli_display_name,
       d.measurement_revision, d.target::text AS target, d.rolling_window_seconds, d.cadence_seconds,
       d.activated_at, d.evaluation_valid_from, d.historical_requested_by, d.historical_requested_at,
       measurement.effective_from AS sli_valid_from
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_services service ON service.id=d.service_id
JOIN public.almanac_slis sli ON sli.id=d.sli_id
JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=d.sli_id AND measurement.revision=d.measurement_revision
WHERE (s.created_at,s.id) > ($1,$2::uuid) AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id))
ORDER BY s.created_at,s.id LIMIT $3;

-- name: InsertSlo :exec
INSERT INTO public.almanac_slos(id,name,display_name,created_at,updated_at) VALUES ($1,$2,$3,$4,$4);

-- name: InsertSloDefinition :exec
INSERT INTO public.almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
VALUES ($1,1,$2,$3,$4,sqlc.arg(target)::text::numeric(38,12),$5,$6,$7,$8,$9,$10);

-- name: UpdateSloMetadata :one
UPDATE public.almanac_slos SET display_name=$1,version=version+1,updated_at=$2
WHERE id=$3 AND version=$4 RETURNING id;

-- name: FindObjectiveCreateReceipt :one
SELECT request_digest,result_id,result,expires_at FROM public.almanac_objective_create_receipts
WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE;

-- name: DeleteObjectiveCreateReceipt :exec
DELETE FROM public.almanac_objective_create_receipts WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3 AND expires_at <= $4;

-- name: InsertObjectiveCreateReceipt :exec
INSERT INTO public.almanac_objective_create_receipts(actor_id,operation,idempotency_key,request_digest,result_id,result,created_at,expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: AggregateSloObservations :one
SELECT count(*) FILTER (WHERE c.outcome='observed')::bigint AS observed_intervals,
       count(*) FILTER (WHERE c.outcome='complete_empty')::bigint AS known_empty_intervals,
       count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0)::bigint AS zero_traffic_intervals,
       COALESCE(sum(v.good_count), 0)::text AS good, COALESCE(sum(v.total_count), 0)::text AS total,
       (min(c.interval_start) FILTER (WHERE c.outcome='observed'))::timestamptz AS first_observation_at,
       (max(c.interval_end) FILTER (WHERE c.outcome='observed'))::timestamptz AS last_observation_at,
       max(c.recorded_at)::timestamptz AS latest_receipt_at
FROM public.almanac_sli_coverage c
LEFT JOIN public.almanac_sli_observation_values v ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision
WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.interval_start >= $3 AND c.interval_end <= $4
  AND c.outcome IN ('observed','complete_empty');

-- name: ListSloTimelineFacts :many
SELECT c.interval_start,c.interval_end,c.outcome,COALESCE(v.good_count, 0)::text AS good,COALESCE(v.total_count, 0)::text AS total,c.recorded_at
FROM public.almanac_sli_coverage c
LEFT JOIN public.almanac_sli_observation_values v ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision
WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.interval_start >= $3 AND c.interval_end <= $4
  AND c.outcome IN ('observed','complete_empty') ORDER BY c.interval_start;

-- name: ListOperationalSloDefinitions :many
SELECT s.id, s.name, s.display_name, s.version, s.definition_revision,
       s.created_at, s.updated_at, d.service_id, service.display_name AS service_display_name,
       d.sli_id, sli.name AS sli_name, sli.display_name AS sli_display_name,
       d.measurement_revision, d.target::text AS target, d.rolling_window_seconds,
       d.cadence_seconds, d.activated_at, d.evaluation_valid_from,
       d.historical_requested_by, d.historical_requested_at,
       measurement.effective_from AS sli_valid_from, measurement.good_query, measurement.total_query,
       datasource.id AS datasource_id, datasource.display_name AS datasource_display_name,
       datasource.polling_interval_seconds, datasource.settling_delay_seconds
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_services service ON service.id=d.service_id
JOIN public.almanac_slis sli ON sli.id=d.sli_id
JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=d.sli_id AND measurement.revision=d.measurement_revision
JOIN public.almanac_datasources datasource ON datasource.id=measurement.datasource_id
WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name || ' ' || s.display_name), lower(sqlc.narg(query_text)::text)) > 0)
  AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
ORDER BY lower(s.name), s.id LIMIT $1;

-- name: GetSloOperationalContext :one
SELECT sli.id AS sli_id, sli.name AS sli_name, sli.display_name AS sli_display_name,
       m.revision AS measurement_revision, m.good_query, m.total_query,
       datasource.id AS datasource_id, datasource.display_name AS datasource_display_name,
       datasource.polling_interval_seconds, datasource.settling_delay_seconds
FROM public.almanac_sli_measurements m
JOIN public.almanac_slis sli ON sli.id=m.sli_id
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
WHERE m.sli_id=$1 AND m.revision=$2;

-- name: ListOperationalSloFacts :many
SELECT c.interval_start, c.interval_end, c.outcome,
       CASE WHEN c.outcome='observed' THEN COALESCE(v.good_count::text, '') ELSE '' END::text AS good,
       CASE WHEN c.outcome='observed' THEN COALESCE(v.total_count::text, '') ELSE '' END::text AS total,
       c.recorded_at
FROM public.almanac_sli_coverage c
LEFT JOIN public.almanac_sli_observation_values v
  ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision
WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.interval_start >= $3 AND c.interval_end <= $4
ORDER BY c.interval_start LIMIT $5;

-- name: DeleteExpiredSloOverviewSnapshots :exec
WITH expired AS (
    SELECT snapshot.id FROM public.almanac_slo_overview_snapshots snapshot
    WHERE snapshot.expires_at <= $1 ORDER BY snapshot.expires_at, snapshot.id LIMIT $2 FOR UPDATE SKIP LOCKED
)
DELETE FROM public.almanac_slo_overview_snapshots s USING expired e WHERE s.id=e.id;

-- name: CountLiveSloOverviewSnapshotsForSession :one
SELECT count(*)::bigint FROM public.almanac_slo_overview_snapshots
WHERE actor_id=$1 AND session_verifier=$2 AND expires_at>$3;

-- name: CountLiveSloOverviewSnapshots :one
SELECT count(*)::bigint FROM public.almanac_slo_overview_snapshots WHERE expires_at>$1;

-- name: InsertSloOverviewSnapshot :exec
INSERT INTO public.almanac_slo_overview_snapshots
(id,cursor_secret,actor_id,session_verifier,role,query_text,service_id,urgency,page_limit,group_counts,state,payload_bytes,created_at,expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'preparing',0,$11,$12);

-- name: GetSloOverviewSnapshot :one
SELECT id,cursor_secret,actor_id,session_verifier,role,query_text,service_id,urgency,page_limit,
       group_counts,state,payload_bytes,created_at,expires_at
FROM public.almanac_slo_overview_snapshots WHERE id=$1 FOR UPDATE;

-- name: InsertSloOverviewSnapshotItem :exec
INSERT INTO public.almanac_slo_overview_snapshot_items(snapshot_id,ordinal,urgency,payload,payload_bytes)
VALUES ($1,$2,$3,$4,$5);

-- name: FinishSloOverviewSnapshot :exec
UPDATE public.almanac_slo_overview_snapshots SET state='ready',payload_bytes=$2 WHERE id=$1 AND state='preparing';

-- name: ListSloOverviewSnapshotItems :many
SELECT ordinal, payload FROM public.almanac_slo_overview_snapshot_items
WHERE snapshot_id=$1 AND ordinal >= $2 ORDER BY ordinal LIMIT $3;

-- name: DeleteSloOverviewSnapshot :exec
DELETE FROM public.almanac_slo_overview_snapshots WHERE id=$1;
