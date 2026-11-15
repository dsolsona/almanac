-- Series only reads immutable evidence. Effective-cutoff index bounds the scan;
-- one extra row detects unsupported density rather than silently truncating it.
-- name: ListAlertSeriesEvaluations :many
SELECT * FROM public.almanac_alert_evaluations
WHERE policy_id=$1 AND revision=$2 AND effective_cutoff >= $3 AND effective_cutoff <= $4
ORDER BY effective_cutoff,id LIMIT $5;

-- name: ReadAlertSeriesRevision :one
SELECT * FROM public.almanac_alert_policy_revisions WHERE policy_id=$1 AND revision=$2;

-- Scheduled ranges are disjoint within a policy revision. At most one range
-- beginning before the requested interval can overlap it; the existing unique
-- (policy_id,revision,starts_at) index supports that predecessor and scoped scan.
-- Do not interpret these bounds as effective cutoffs. Unknown cutoff absence is
-- reported as not retained, not inferred from unrelated historical work ranges.
-- name: ListAlertSeriesRanges :many
WITH predecessor AS (
 SELECT r.* FROM public.almanac_alert_ranges r
 WHERE r.policy_id=$1 AND r.revision=$2 AND r.starts_at < sqlc.arg(start_at)::timestamptz
 ORDER BY r.starts_at DESC LIMIT 1
), scoped AS (
 SELECT p.* FROM predecessor p WHERE p.ends_at > sqlc.arg(start_at)::timestamptz
 UNION ALL
 (SELECT r.* FROM public.almanac_alert_ranges r
 WHERE r.policy_id=$1 AND r.revision=$2 AND r.starts_at >= sqlc.arg(start_at)::timestamptz
 AND r.starts_at <= sqlc.arg(end_at)::timestamptz
 ORDER BY r.starts_at LIMIT sqlc.arg(row_limit))
)
SELECT * FROM scoped ORDER BY starts_at,id LIMIT sqlc.arg(row_limit);
