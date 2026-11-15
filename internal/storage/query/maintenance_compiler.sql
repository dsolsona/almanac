-- name: TouchMaintenanceInputGuard :exec
INSERT INTO public.almanac_maintenance_input_guards(slo_id) VALUES($1)
ON CONFLICT(slo_id) DO UPDATE SET touched=NOT almanac_maintenance_input_guards.touched;
-- name: CaptureMaintenanceBuild :one
INSERT INTO public.almanac_maintenance_builds(id,slo_id,target_revision,created_at) VALUES($1,$2,$3,$4)
ON CONFLICT(slo_id,target_revision) DO UPDATE SET slo_id=excluded.slo_id RETURNING *;
-- name: LockMaintenanceBuild :one
SELECT * FROM public.almanac_maintenance_builds WHERE id=$1 FOR UPDATE;
-- name: ReadMaintenanceBuild :one
SELECT * FROM public.almanac_maintenance_builds WHERE id=$1;
-- name: LeaseMaintenanceBuild :exec
UPDATE public.almanac_maintenance_builds SET lease_token=$2,lease_until=$3 WHERE id=$1;
-- name: ReadMaintenanceCompilerEvents :many
WITH page AS MATERIALIZED (
 SELECT c.* FROM public.almanac_slo_exclusion_changes c WHERE c.slo_id=$1 AND c.revision>sqlc.arg(after_revision)::bigint AND c.revision<=sqlc.arg(target_revision)::bigint ORDER BY c.revision LIMIT sqlc.arg(page_limit)::integer
)
SELECT p.*,r.starts_at,r.ends_at,r.tombstone,
 EXISTS(SELECT 1 FROM public.almanac_maintenance_exclusion_scopes s WHERE s.exclusion_id=p.exclusion_id AND s.revision=p.exclusion_revision AND s.slo_id=p.slo_id) AS scoped
FROM page p LEFT JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id=p.exclusion_id AND r.revision=p.exclusion_revision ORDER BY p.revision;
-- name: PutMaintenanceCompilerMember :exec
INSERT INTO public.almanac_maintenance_members(build_id,exclusion_id,exclusion_revision,slo_id,starts_at,ends_at) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(build_id,exclusion_id) DO UPDATE SET exclusion_revision=excluded.exclusion_revision,starts_at=excluded.starts_at,ends_at=excluded.ends_at;
-- name: RemoveMaintenanceCompilerMember :exec
DELETE FROM public.almanac_maintenance_members WHERE build_id=$1 AND exclusion_id=$2;
-- name: AdvanceMaintenanceReplay :exec
UPDATE public.almanac_maintenance_builds SET raw_cursor=$2,phase=$3,lease_token=NULL,lease_until=NULL WHERE id=$1;
-- name: ReadMaintenanceCompilerMembers :many
SELECT * FROM public.almanac_maintenance_members WHERE build_id=$1
 AND (starts_at,exclusion_id)>(sqlc.arg(after_start)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY starts_at,exclusion_id LIMIT sqlc.arg(page_limit)::integer;
-- name: InsertMaintenanceCoverage :exec
INSERT INTO public.almanac_maintenance_coverage(build_id,starts_at,ends_at,cumulative_us) VALUES($1,$2,$3,$4);
-- name: AdvanceMaintenanceUnion :exec
UPDATE public.almanac_maintenance_builds SET member_start=sqlc.narg(member_start),member_id=sqlc.narg(member_id),
 frontier_start=sqlc.narg(frontier_start),frontier_end=sqlc.narg(frontier_end),cumulative_us=sqlc.arg(cumulative_us)::bigint,phase=sqlc.arg(phase)::text,completed_at=sqlc.narg(completed_at),lease_token=NULL,lease_until=NULL WHERE id=sqlc.arg(id)::uuid;
-- name: ReadMaintenanceCoveragePredecessor :one
SELECT * FROM public.almanac_maintenance_coverage WHERE build_id=$1 AND starts_at<sqlc.arg(at)::timestamptz ORDER BY starts_at DESC LIMIT 1;
