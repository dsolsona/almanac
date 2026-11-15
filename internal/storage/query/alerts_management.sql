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
