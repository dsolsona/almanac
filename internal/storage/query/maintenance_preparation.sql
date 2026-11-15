-- name: EnqueueMaintenancePreparation :exec
INSERT INTO public.almanac_maintenance_preparation(id,kind,slo_id,target_revision,build_id,second_build,sli_id,definition_revision,measurement_revision,hour_start,source_generation,priority,available_at,created_at,updated_at)
VALUES(sqlc.arg(id),sqlc.arg(kind),sqlc.arg(slo_id),sqlc.arg(target_revision),sqlc.arg(build_id),sqlc.narg(second_build),sqlc.narg(sli_id),sqlc.narg(definition_revision),sqlc.narg(measurement_revision),sqlc.narg(hour_start),sqlc.narg(source_generation),sqlc.arg(priority),sqlc.arg(now),sqlc.arg(now),sqlc.arg(now))
ON CONFLICT(kind,slo_id,target_revision,build_id,second_build,sli_id,definition_revision,measurement_revision,hour_start,source_generation)
DO UPDATE SET priority=CASE WHEN excluded.priority='live' THEN 'live' ELSE almanac_maintenance_preparation.priority END;

-- name: LockMaintenancePreparationDue :many
SELECT * FROM public.almanac_maintenance_preparation
WHERE state='pending' AND priority=$1 AND available_at<=sqlc.arg(now)::timestamptz
 AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
 AND (last_tick IS NULL OR last_tick<sqlc.arg(tick)::timestamptz)
ORDER BY available_at,id LIMIT sqlc.arg(row_limit)::integer FOR UPDATE SKIP LOCKED;
-- name: LeaseMaintenancePreparation :one
UPDATE public.almanac_maintenance_preparation SET lease_token=$2,lease_epoch=lease_epoch+1,lease_until=$3,last_tick=$4,updated_at=$5 WHERE id=$1 RETURNING *;
-- name: LockMaintenancePreparation :one
SELECT * FROM public.almanac_maintenance_preparation WHERE id=$1 FOR UPDATE;
-- name: FinishMaintenancePreparation :execrows
UPDATE public.almanac_maintenance_preparation SET state=sqlc.arg(state),reason=sqlc.arg(reason),attempts=sqlc.arg(attempts),available_at=sqlc.arg(available_at),updated_at=sqlc.arg(now),lease_token=NULL,lease_until=NULL
WHERE id=sqlc.arg(id) AND state='pending' AND lease_token=sqlc.arg(lease_token) AND lease_epoch=sqlc.arg(lease_epoch) AND lease_until>sqlc.arg(now)::timestamptz;

-- name: EnqueueMissingMaintenanceSourceHours :exec
-- Never accelerate or reset canonical retries, and never rebuild healthy hours.
-- The caller admits/deduplicates hours and holds SLI authority before this query.
INSERT INTO public.almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT g.sli_id,g.measurement_revision,g.generation,h,h+interval '1 hour',sqlc.arg(now)::timestamptz,sqlc.arg(now)::timestamptz
FROM public.almanac_sli_source_generations g CROSS JOIN unnest(sqlc.arg(hours)::timestamptz[]) h
WHERE g.sli_id=sqlc.arg(sli_id) AND g.measurement_revision=sqlc.arg(measurement_revision)
 AND NOT EXISTS(SELECT 1 FROM public.almanac_sli_hourly_aggregates a WHERE a.sli_id=g.sli_id AND a.measurement_revision=g.measurement_revision AND a.hour_start=h)
 AND EXISTS(SELECT 1 FROM public.almanac_sli_observations o WHERE o.sli_id=g.sli_id AND o.measurement_revision=g.measurement_revision AND o.interval_start>=h AND o.interval_start<h+interval '1 hour')
ON CONFLICT(sli_id,measurement_revision,range_start) DO NOTHING;
