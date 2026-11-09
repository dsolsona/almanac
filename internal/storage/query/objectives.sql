-- name: GetService :one
SELECT * FROM public.almanac_services WHERE id = $1 AND archived_at IS NULL;

-- name: ListServices :many
SELECT * FROM public.almanac_services
WHERE archived_at IS NULL AND (created_at, id) > ($1, $2::uuid) ORDER BY created_at, id LIMIT $3;

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
WITH definition AS (
INSERT INTO public.almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
VALUES ($1,1,$2,$3,$4,sqlc.arg(target)::text::numeric(38,12),$5,$6,$7,$8,$9,$10)
RETURNING *
)
INSERT INTO public.almanac_slo_extrema_cursor
    (slo_id,definition_revision,measurement_revision,calculation_version,built_through,next_attempt_at)
SELECT d.slo_id,d.revision,d.measurement_revision,'occurrences-operational-v1',
       date_trunc('hour',GREATEST(d.evaluation_valid_from,m.effective_from) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',d.activated_at
FROM definition d JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision;

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
       count(*) FILTER (WHERE c.outcome='observed' AND c.total_count=0)::bigint AS zero_traffic_intervals,
       COALESCE(sum(c.good_count), 0)::text AS good, COALESCE(sum(c.total_count), 0)::text AS total,
       (min(c.interval_start) FILTER (WHERE c.outcome='observed'))::timestamptz AS first_observation_at,
       (max(c.interval_start+datasource.polling_interval_seconds*interval '1 second') FILTER (WHERE c.outcome='observed'))::timestamptz AS last_observation_at,
       max(c.recorded_at)::timestamptz AS latest_receipt_at
FROM public.almanac_sli_observations c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.interval_start>=sqlc.arg(interval_start)::timestamptz
  AND c.interval_start+datasource.polling_interval_seconds*interval '1 second'<=sqlc.arg(interval_end)::timestamptz
  AND c.outcome IN ('observed','complete_empty');

-- name: ListSloTimelineFacts :many
SELECT c.interval_start,(c.interval_start+datasource.polling_interval_seconds*interval '1 second')::timestamptz AS interval_end,
       c.outcome,COALESCE(c.good_count, 0)::text AS good,COALESCE(c.total_count, 0)::text AS total,c.recorded_at
FROM public.almanac_sli_observations c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
WHERE c.sli_id=$1 AND c.measurement_revision=$2 AND c.interval_start>=sqlc.arg(interval_start)::timestamptz
  AND c.interval_start+datasource.polling_interval_seconds*interval '1 second'<=sqlc.arg(interval_end)::timestamptz
  AND c.outcome IN ('observed','complete_empty') ORDER BY c.interval_start;

-- name: GetSloOperationalContext :one
SELECT sli.id AS sli_id, sli.name AS sli_name, sli.display_name AS sli_display_name,
       m.revision AS measurement_revision, m.good_query, m.total_query,
       datasource.id AS datasource_id, datasource.display_name AS datasource_display_name,
       datasource.polling_interval_seconds, datasource.settling_delay_seconds
FROM public.almanac_sli_measurements m
JOIN public.almanac_slis sli ON sli.id=m.sli_id
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
WHERE m.sli_id=$1 AND m.revision=$2;
