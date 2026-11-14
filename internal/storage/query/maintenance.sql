-- name: LockMaintenanceIdentity :one
SELECT * FROM public.almanac_maintenance_exclusions WHERE id=$1 FOR UPDATE;
-- name: LockMaintenanceSlo :one
SELECT id FROM public.almanac_slos WHERE id=$1 FOR NO KEY UPDATE;
-- name: LockMaintenanceState :one
SELECT revision FROM public.almanac_slo_exclusion_state WHERE slo_id=$1 FOR UPDATE;
-- name: FenceMaintenanceState :one
SELECT revision FROM public.almanac_slo_exclusion_state WHERE slo_id=$1 FOR SHARE;
-- name: ReadMaintenanceState :one
SELECT revision FROM public.almanac_slo_exclusion_state WHERE slo_id=$1;
-- name: ReadMaintenanceRevision :one
SELECT r.*,e.creator_id,e.created_at,e.updated_at FROM public.almanac_maintenance_exclusions e
JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id=e.id
AND r.revision=CASE WHEN sqlc.arg(revision)::bigint=0 THEN e.current_revision ELSE sqlc.arg(revision)::bigint END
WHERE e.id=sqlc.arg(id);
-- name: ListMaintenanceScopes :many
SELECT slo_id FROM public.almanac_maintenance_exclusion_scopes WHERE exclusion_id=$1 AND revision=$2 ORDER BY slo_id;
-- name: ListMaintenanceIdentities :many
SELECT id,created_at FROM public.almanac_maintenance_exclusions
WHERE (created_at,id)>(sqlc.arg(after_time)::timestamptz,sqlc.arg(after_id)::uuid) ORDER BY created_at,id LIMIT sqlc.arg(page_limit);
-- name: ListMaintenanceGrids :many
SELECT d.slo_id,d.revision,d.cadence_seconds,d.activated_at,d.evaluation_valid_from,m.effective_from,m.measurement_kind,m.component_mode
FROM public.almanac_slo_definitions d JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision
WHERE d.slo_id=$1 ORDER BY d.revision;
-- name: InsertMaintenanceIdentity :exec
INSERT INTO public.almanac_maintenance_exclusions(id,current_revision,creator_id,created_at,updated_at) VALUES ($1,1,$2,$3,$3);
-- name: AdvanceMaintenanceIdentity :exec
UPDATE public.almanac_maintenance_exclusions SET current_revision=$2,updated_at=$3 WHERE id=$1;
-- name: InsertMaintenanceAudit :one
INSERT INTO public.almanac_audit_events(actor,subject_id,action,outcome,occurred_at,request_id) VALUES ($1,$2,$3,'success',$4,$5) RETURNING id;
-- name: InsertMaintenanceRevision :exec
INSERT INTO public.almanac_maintenance_exclusion_revisions(exclusion_id,revision,title,reason,starts_at,ends_at,operation,tombstone,actor_id,recorded_at,request_id,audit_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);
-- name: InsertMaintenanceScope :exec
INSERT INTO public.almanac_maintenance_exclusion_scopes(exclusion_id,revision,slo_id) VALUES ($1,$2,$3);
-- name: AdvanceMaintenanceState :one
UPDATE public.almanac_slo_exclusion_state SET revision=revision+1,changed_at=$2 WHERE slo_id=$1 AND revision<9223372036854775807 RETURNING revision;
-- name: InsertMaintenanceChange :exec
INSERT INTO public.almanac_slo_exclusion_changes(slo_id,revision,exclusion_id,exclusion_revision,included,coverage_changed,recorded_at) VALUES ($1,$2,$3,$4,$5,$6,$7);
-- name: EnqueueMaintenanceWork :exec
INSERT INTO public.almanac_slo_exclusion_work(slo_id,desired_revision,work_version,affected_start,affected_end,next_attempt_at) VALUES ($1,$2,1,$3,$4,$5)
ON CONFLICT (slo_id) DO UPDATE SET desired_revision=excluded.desired_revision,work_version=almanac_slo_exclusion_work.work_version+1,
 affected_start=LEAST(almanac_slo_exclusion_work.affected_start,excluded.affected_start),affected_end=GREATEST(almanac_slo_exclusion_work.affected_end,excluded.affected_end),next_attempt_at=LEAST(almanac_slo_exclusion_work.next_attempt_at,excluded.next_attempt_at);
-- name: AcknowledgeMaintenanceWork :execrows
DELETE FROM public.almanac_slo_exclusion_work WHERE slo_id=$1 AND desired_revision=$2 AND work_version=$3;
-- name: ReadMaintenanceSnapshot :many
WITH latest AS (
 SELECT DISTINCT ON (exclusion_id) exclusion_id,exclusion_revision,included FROM public.almanac_slo_exclusion_changes
 WHERE slo_id=$1 AND revision<=sqlc.arg(revision)::bigint ORDER BY exclusion_id,revision DESC
)
SELECT r.* FROM latest c JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id=c.exclusion_id AND r.revision=c.exclusion_revision
WHERE c.included AND NOT r.tombstone AND r.starts_at<sqlc.arg(window_end)::timestamptz AND r.ends_at>sqlc.arg(window_start)::timestamptz
ORDER BY r.starts_at,r.ends_at,r.exclusion_id;
