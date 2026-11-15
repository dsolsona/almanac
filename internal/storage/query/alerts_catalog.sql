-- name: ListAlertCatalog :many
WITH filtered AS (
 SELECT c.*,
 CASE WHEN sqlc.arg(group_slo)::boolean THEN c.slo_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END AS group_key,
 CASE WHEN NOT sqlc.arg(sort_severity)::boolean THEN 0 WHEN c.severity='high' THEN 0 WHEN c.severity='medium' THEN 1 ELSE 2 END::integer AS priority_key,
 (-extract(epoch FROM c.opened_at)*1000000)::bigint AS time_key
 FROM public.almanac_alert_catalog c
 WHERE c.state=ANY(sqlc.arg(states)::text[]) AND c.severity=ANY(sqlc.arg(severities)::text[])
 AND (sqlc.narg(slo_id)::uuid IS NULL OR c.slo_id=sqlc.narg(slo_id))
 AND (sqlc.narg(service_id)::uuid IS NULL OR c.service_id=sqlc.narg(service_id))
 AND (sqlc.arg(search)::text='' OR strpos(lower(c.policy_name||' '||c.slo_name||' '||c.service_name),sqlc.arg(search))>0)
), page AS (
 SELECT * FROM filtered WHERE NOT sqlc.arg(has_cursor)::boolean OR
 (group_key,priority_key,time_key,id)>(sqlc.arg(after_group)::uuid,sqlc.arg(after_priority)::integer,sqlc.arg(after_time)::bigint,sqlc.arg(after_id)::uuid)
 ORDER BY group_key,priority_key,time_key,id LIMIT sqlc.arg(page_limit)
)
SELECT page.*,o.version,o.transition_sequence,o.payload,o.policy_snapshot,
 e.id AS evaluation_id,e.evidence,e.scheduled_at,e.committed_at,
 c.schedulable,c.next_scheduled_at,c.available_at,c.pending_cutoff,c.last_cutoff,c.last_evaluation_id,c.waiting_reason,c.failure_attempts,c.exhausted
FROM page JOIN public.almanac_alert_occurrences o ON o.id=page.id
JOIN public.almanac_alert_evaluations e ON e.id=o.latest_evaluation_id
JOIN public.almanac_alert_cursors c ON (c.policy_id,c.revision)=(o.policy_id,o.revision)
ORDER BY page.group_key,page.priority_key,page.time_key,page.id;

-- name: CountAlertCatalog :one
SELECT count(*)::bigint FROM public.almanac_alert_catalog c
WHERE c.state=ANY(sqlc.arg(states)::text[]) AND c.severity=ANY(sqlc.arg(severities)::text[])
AND (sqlc.narg(slo_id)::uuid IS NULL OR c.slo_id=sqlc.narg(slo_id))
AND (sqlc.narg(service_id)::uuid IS NULL OR c.service_id=sqlc.narg(service_id))
AND (sqlc.arg(search)::text='' OR strpos(lower(c.policy_name||' '||c.slo_name||' '||c.service_name),sqlc.arg(search))>0);

-- name: GroupAlertCatalog :many
SELECT c.slo_id,c.slo_name,count(*)::bigint AS total FROM public.almanac_alert_catalog c
WHERE c.state=ANY(sqlc.arg(states)::text[]) AND c.severity=ANY(sqlc.arg(severities)::text[])
AND (sqlc.narg(slo_id)::uuid IS NULL OR c.slo_id=sqlc.narg(slo_id))
AND (sqlc.narg(service_id)::uuid IS NULL OR c.service_id=sqlc.narg(service_id))
AND (sqlc.arg(search)::text='' OR strpos(lower(c.policy_name||' '||c.slo_name||' '||c.service_name),sqlc.arg(search))>0)
AND c.slo_id=ANY(sqlc.arg(page_slos)::uuid[])
GROUP BY c.slo_id,c.slo_name ORDER BY c.slo_id;

-- name: ListAlertTransitionHistory :many
SELECT * FROM public.almanac_alert_transitions WHERE occurrence_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3;

-- name: ListAlertRangeHistory :many
SELECT * FROM public.almanac_alert_ranges WHERE policy_id=$1 AND revision=$2
AND (NOT sqlc.arg(has_cursor)::boolean OR (starts_at,id)>(sqlc.arg(after_start)::timestamptz,sqlc.arg(after_id)::uuid))
ORDER BY starts_at,id LIMIT sqlc.arg(page_limit);

-- name: AlertPolicyRevisionExists :one
SELECT EXISTS(SELECT 1 FROM public.almanac_alert_policy_revisions WHERE policy_id=$1 AND revision=$2);
