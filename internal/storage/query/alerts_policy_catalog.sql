-- name: ListAlertPolicies :many
WITH page AS (
 SELECT p.* FROM public.almanac_alert_policies p
 WHERE (sqlc.narg(slo_id)::uuid IS NULL OR p.slo_id=sqlc.narg(slo_id))
 AND (sqlc.narg(enabled)::boolean IS NULL OR p.enabled=sqlc.narg(enabled))
 AND (sqlc.arg(severity)::text='' OR p.severity=sqlc.arg(severity))
 AND (sqlc.arg(search)::text='' OR strpos(lower(p.name),sqlc.arg(search))>0)
 AND (NOT sqlc.arg(has_cursor)::boolean OR (p.created_at,p.id)<(sqlc.arg(after_created)::timestamptz,sqlc.arg(after_id)::uuid))
 ORDER BY p.created_at DESC,p.id DESC LIMIT sqlc.arg(page_limit)
)
SELECT p.*,r.conditions,s.display_name AS slo_name,
 c.schedulable,c.next_scheduled_at,c.available_at,c.pending_cutoff,c.last_cutoff,c.last_evaluation_id,c.waiting_reason,c.failure_attempts,c.exhausted,c.open_occurrence_id
FROM page p JOIN public.almanac_alert_policy_revisions r ON (r.policy_id,r.revision)=(p.id,p.evaluation_revision)
JOIN public.almanac_slos s ON s.id=r.slo_id
JOIN public.almanac_alert_cursors c ON (c.policy_id,c.revision)=(p.id,p.evaluation_revision)
ORDER BY p.created_at DESC,p.id DESC;

-- name: CountAlertPolicies :one
SELECT count(*)::bigint FROM public.almanac_alert_policies p
WHERE (sqlc.narg(slo_id)::uuid IS NULL OR p.slo_id=sqlc.narg(slo_id))
AND (sqlc.narg(enabled)::boolean IS NULL OR p.enabled=sqlc.narg(enabled))
AND (sqlc.arg(severity)::text='' OR p.severity=sqlc.arg(severity))
AND (sqlc.arg(search)::text='' OR strpos(lower(p.name),sqlc.arg(search))>0);
