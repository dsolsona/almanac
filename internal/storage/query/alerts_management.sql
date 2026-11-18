-- name: ReadAlertPolicy :one
SELECT p.*,r.conditions FROM public.almanac_alert_policies p
JOIN public.almanac_alert_policy_revisions r ON r.policy_id=p.id AND r.revision=p.evaluation_revision
WHERE p.id=$1;

-- name: ReadAlertOccurrence :one
SELECT * FROM public.almanac_alert_occurrences WHERE id=$1;

-- name: ReadAlertEvaluation :one
SELECT * FROM public.almanac_alert_evaluations WHERE id=$1;

-- Read-only lookup deliberately does not lock the receipt row.
-- name: LookupAlertCommand :one
SELECT result,expires_at FROM public.almanac_management_receipts
WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3;

-- name: ListAlertMaintenanceSuppressions :many
SELECT id, effective_cutoff, scheduled_at, committed_at, (evidence #> '{Evaluation,Maintenance}')::text AS suppression
FROM public.almanac_alert_evaluations
WHERE policy_id = sqlc.arg(policy_id) AND revision = sqlc.arg(revision)
  AND evidence @> '{"Evaluation":{"Maintenance":{"OpeningSuppressed":true}}}'::jsonb
  AND (NOT sqlc.arg(has_cursor)::boolean OR
    (effective_cutoff, id) < (sqlc.arg(after_cutoff)::timestamptz, sqlc.arg(after_id)::uuid))
ORDER BY effective_cutoff DESC, id DESC
LIMIT sqlc.arg(page_limit)::integer;
