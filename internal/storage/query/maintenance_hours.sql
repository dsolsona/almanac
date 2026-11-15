-- Preparation uses caller READ COMMITTED; facts are read after authority locks.
-- name: LockMaintenanceHourSli :one
SELECT id FROM public.almanac_slis WHERE id=$1 FOR SHARE;
-- name: LockMaintenanceHourSlo :one
SELECT id FROM public.almanac_slos WHERE id=$1 FOR SHARE;
-- name: LockMaintenanceHourBuild :one
SELECT * FROM public.almanac_maintenance_builds WHERE id=$1 FOR SHARE;
-- name: ReadMaintenanceHourDefinition :one
SELECT d.sli_id,d.measurement_revision,d.cadence_seconds,m.measurement_kind,m.component_mode
FROM public.almanac_slo_definitions d JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision
WHERE d.slo_id=$1 AND d.revision=$2;
-- name: LockMaintenanceHourSource :one
SELECT source_generation,good::text AS good,total::text AS total,observed_intervals,known_empty_intervals,zero_traffic_intervals,first_interval_start,last_interval_end,first_observation_at,last_observation_at
FROM public.almanac_sli_hourly_aggregates
WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$3 FOR SHARE;
-- name: ReadMaintenanceHourState :one
SELECT coalesce((SELECT h.source_generation FROM public.almanac_sli_hourly_aggregates h WHERE h.sli_id=sqlc.arg(sli_id) AND h.measurement_revision=sqlc.arg(measurement_revision) AND h.hour_start=sqlc.arg(hour_start)),0)::bigint AS source_generation,
 EXISTS(SELECT 1 FROM public.almanac_sli_observations o WHERE o.sli_id=sqlc.arg(sli_id) AND o.measurement_revision=sqlc.arg(measurement_revision) AND o.interval_start>=sqlc.arg(hour_start)::timestamptz AND o.interval_start<sqlc.arg(hour_start)::timestamptz+interval '1 hour') AS raw_present,
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision) AND d.range_start<sqlc.arg(hour_start)::timestamptz+interval '1 hour' AND d.range_end>sqlc.arg(hour_start)::timestamptz) AS pending,
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision) AND d.range_start<sqlc.arg(hour_start)::timestamptz+interval '1 hour' AND d.range_end>sqlc.arg(hour_start)::timestamptz AND d.attempts>=5 AND (lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)) AS exhausted;
-- name: ReadMaintenanceHourRaw :many
SELECT interval_start,outcome,coalesce(good_count,0)::numeric::text AS good,coalesce(total_count,0)::numeric::text AS total,recorded_at,ingestion_generation
FROM public.almanac_sli_observations
WHERE sli_id=$1 AND measurement_revision=$2 AND interval_start>=sqlc.arg(hour_start)::timestamptz AND interval_start<sqlc.arg(hour_start)::timestamptz+interval '1 hour'
ORDER BY interval_start LIMIT 3601;
-- name: ReadMaintenanceHourFragments :many
-- A backward predecessor plus a bounded forward PK range; never a revision scan.
SELECT starts_at,ends_at FROM (
 (SELECT c.starts_at,c.ends_at FROM public.almanac_maintenance_coverage c WHERE c.build_id=sqlc.arg(build_id) AND c.starts_at<sqlc.arg(hour_start)::timestamptz ORDER BY c.starts_at DESC LIMIT 1)
 UNION ALL
 (SELECT c.starts_at,c.ends_at FROM public.almanac_maintenance_coverage c WHERE c.build_id=sqlc.arg(build_id) AND c.starts_at>=sqlc.arg(hour_start)::timestamptz AND c.starts_at<sqlc.arg(hour_start)::timestamptz+interval '1 hour' ORDER BY c.starts_at LIMIT 3601)
) fragments WHERE ends_at>sqlc.arg(hour_start)::timestamptz ORDER BY starts_at;
-- name: ReadMaintenanceHour :one
SELECT slo_id,definition_revision,measurement_revision,build_id,hour_start,source_generation,sli_id,exclusion_revision,prepared_at,eligible_good::text AS eligible_good,eligible_total::text AS eligible_total,eligible_expected,eligible_observed,eligible_known_empty,eligible_zero_traffic,eligible_first_observation,eligible_last_observation,eligible_latest_receipt,excluded_good::text AS excluded_good,excluded_total::text AS excluded_total,excluded_expected,excluded_observed,excluded_known_empty,excluded_zero_traffic,excluded_first_observation,excluded_last_observation,excluded_latest_receipt FROM public.almanac_slo_maintenance_hours WHERE slo_id=sqlc.arg(slo_id) AND definition_revision=sqlc.arg(definition_revision) AND measurement_revision=sqlc.arg(measurement_revision) AND build_id=sqlc.arg(build_id) AND hour_start=sqlc.arg(hour_start) AND source_generation=sqlc.arg(source_generation);
-- name: InsertMaintenanceHour :execrows
INSERT INTO public.almanac_slo_maintenance_hours(slo_id,definition_revision,measurement_revision,build_id,hour_start,source_generation,sli_id,exclusion_revision,prepared_at,eligible_good,eligible_total,eligible_expected,eligible_observed,eligible_known_empty,eligible_zero_traffic,eligible_first_observation,eligible_last_observation,eligible_latest_receipt,excluded_good,excluded_total,excluded_expected,excluded_observed,excluded_known_empty,excluded_zero_traffic,excluded_first_observation,excluded_last_observation,excluded_latest_receipt) VALUES(sqlc.arg(slo_id),sqlc.arg(definition_revision),sqlc.arg(measurement_revision),sqlc.arg(build_id),sqlc.arg(hour_start),sqlc.arg(source_generation),sqlc.arg(sli_id),sqlc.arg(exclusion_revision),sqlc.arg(prepared_at),sqlc.arg(eligible_good)::text::numeric,sqlc.arg(eligible_total)::text::numeric,sqlc.arg(eligible_expected),sqlc.arg(eligible_observed),sqlc.arg(eligible_known_empty),sqlc.arg(eligible_zero_traffic),sqlc.narg(eligible_first_observation)::timestamptz,sqlc.narg(eligible_last_observation)::timestamptz,sqlc.narg(eligible_latest_receipt)::timestamptz,sqlc.arg(excluded_good)::text::numeric,sqlc.arg(excluded_total)::text::numeric,sqlc.arg(excluded_expected),sqlc.arg(excluded_observed),sqlc.arg(excluded_known_empty),sqlc.arg(excluded_zero_traffic),sqlc.narg(excluded_first_observation)::timestamptz,sqlc.narg(excluded_last_observation)::timestamptz,sqlc.narg(excluded_latest_receipt)::timestamptz) ON CONFLICT DO NOTHING;

-- name: ReadMaintenanceHourIsolation :one
SELECT current_setting('transaction_isolation')::text AS isolation;
