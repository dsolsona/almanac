-- name: LockMaintenanceAuthorityDue :many
SELECT * FROM public.almanac_slo_exclusion_work
WHERE next_attempt_at<=sqlc.arg(now)::timestamptz
 AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz OR claimed_version<>work_version)
 AND (last_tick IS NULL OR last_tick<sqlc.arg(tick)::timestamptz)
 AND (attempts<sqlc.arg(max_attempts)::integer OR failure_version<>work_version)
ORDER BY next_attempt_at,slo_id LIMIT sqlc.arg(row_limit)::integer FOR UPDATE SKIP LOCKED;
-- name: LeaseMaintenanceAuthority :one
UPDATE public.almanac_slo_exclusion_work SET lease_token=$2,lease_epoch=lease_epoch+1,lease_until=$3,
 claimed_version=work_version,claimed_revision=desired_revision,last_tick=$4,
 attempts=CASE WHEN failure_version<>work_version THEN 0 ELSE attempts END,
 failure_reason=CASE WHEN failure_version<>work_version THEN '' ELSE failure_reason END
WHERE slo_id=$1 RETURNING *;
-- name: ReadMaintenanceAuthorityWork :one
SELECT * FROM public.almanac_slo_exclusion_work WHERE slo_id=$1;
-- name: LockMaintenanceAuthorityWork :one
SELECT * FROM public.almanac_slo_exclusion_work WHERE slo_id=$1 FOR UPDATE;
-- name: ReadMaintenanceAuthorityChange :one
SELECT range_count FROM public.almanac_maintenance_change_sets WHERE slo_id=$1 AND revision=$2;
-- name: ReadMaintenanceAuthorityRanges :many
SELECT part,lower(input_range)::timestamptz AS starts_at,upper(input_range)::timestamptz AS ends_at
FROM public.almanac_maintenance_changed_inputs WHERE slo_id=$1 AND revision=$2
 AND part>sqlc.arg(after_part)::smallint ORDER BY part LIMIT sqlc.arg(row_limit)::integer;
-- name: NextMaintenanceAuthorityBoundary :one
SELECT least(
 (SELECT lower(input_range) FROM public.almanac_maintenance_changed_inputs WHERE almanac_maintenance_changed_inputs.slo_id=sqlc.arg(slo_id) AND lower(input_range)>sqlc.arg(after_at)::timestamptz AND revision<=sqlc.arg(revision)::bigint ORDER BY lower(input_range) LIMIT 1),
 (SELECT upper(input_range) FROM public.almanac_maintenance_changed_inputs WHERE almanac_maintenance_changed_inputs.slo_id=sqlc.arg(slo_id) AND upper(input_range)>sqlc.arg(after_at)::timestamptz AND revision<=sqlc.arg(revision)::bigint ORDER BY upper(input_range) LIMIT 1)
)::timestamptz AS boundary;
-- name: HasMaintenanceAuthorityInput :one
SELECT EXISTS(SELECT 1 FROM public.almanac_maintenance_changed_inputs WHERE slo_id=sqlc.arg(slo_id)
 AND scope_key @> sqlc.arg(scope_ordinal)::bigint
 AND revision_key && int8range(1,CASE WHEN sqlc.arg(revision)::bigint=9223372036854775807 THEN NULL ELSE sqlc.arg(revision)::bigint+1 END,'[)')
 AND input_range && tstzrange(sqlc.arg(start_at)::timestamptz,sqlc.arg(end_at)::timestamptz,'[)') LIMIT 1)::boolean AS changed;
-- name: AdvanceMaintenanceAuthorityWork :execrows
UPDATE public.almanac_slo_exclusion_work SET processed_revision=sqlc.arg(processed_revision),progress_part=sqlc.arg(progress_part),
 progress_hour=sqlc.narg(progress_hour),progress_until=sqlc.narg(progress_until),scheduled_through=sqlc.arg(scheduled_through),
 current_revision=sqlc.arg(current_revision),boundary_through=sqlc.arg(boundary_through),attempts=0,failure_version=work_version,failure_reason='',
 next_attempt_at=sqlc.arg(next_attempt_at),lease_token=NULL,lease_until=NULL
WHERE slo_id=sqlc.arg(slo_id) AND desired_revision=sqlc.arg(desired_revision) AND work_version=sqlc.arg(work_version)
 AND lease_token=sqlc.arg(lease_token) AND lease_epoch=sqlc.arg(lease_epoch) AND lease_until>sqlc.arg(now)::timestamptz;
-- name: FailMaintenanceAuthorityWork :execrows
UPDATE public.almanac_slo_exclusion_work SET attempts=attempts+1,failure_version=work_version,failure_reason='authority_failed',next_attempt_at=sqlc.arg(next_attempt_at),lease_token=NULL,lease_until=NULL
WHERE slo_id=sqlc.arg(slo_id) AND desired_revision=sqlc.arg(desired_revision) AND work_version=sqlc.arg(work_version)
 AND lease_token=sqlc.arg(lease_token) AND lease_epoch=sqlc.arg(lease_epoch) AND lease_until>sqlc.arg(now)::timestamptz;
-- name: ReadMaintenanceAuthorityRetargets :many
SELECT * FROM public.almanac_maintenance_preparation WHERE slo_id=$1
 AND kind IN ('extrema_hour','extrema_day') AND state<>'complete' AND authority_retargeted_revision=0 AND target_revision<$2
ORDER BY id LIMIT sqlc.arg(row_limit)::integer;
-- name: MarkMaintenanceAuthorityRetarget :execrows
UPDATE public.almanac_maintenance_preparation SET authority_retargeted_revision=$2
WHERE id=$1 AND authority_retargeted_revision=0 AND target_revision<$2;
-- name: ReleaseMaintenanceAuthorityWork :execrows
UPDATE public.almanac_slo_exclusion_work SET next_attempt_at=sqlc.arg(next_attempt_at),lease_token=NULL,lease_until=NULL
WHERE slo_id=sqlc.arg(slo_id) AND desired_revision=sqlc.arg(desired_revision) AND work_version=sqlc.arg(work_version)
 AND lease_token=sqlc.arg(lease_token) AND lease_epoch=sqlc.arg(lease_epoch) AND lease_until>sqlc.arg(now)::timestamptz;
