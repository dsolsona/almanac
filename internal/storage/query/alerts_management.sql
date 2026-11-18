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

-- Read a bounded range through the existing policy/revision/cutoff key,
-- then inspect JSON for only those evaluations. A page can be sparse.
-- name: ScanAlertMaintenanceSuppressions :many
WITH source_batch AS MATERIALIZED (
    SELECT id, effective_cutoff, scheduled_at, committed_at, evidence
    FROM public.almanac_alert_evaluations
    WHERE policy_id = sqlc.arg(policy_id) AND revision = sqlc.arg(revision)
      AND effective_cutoff < coalesce(sqlc.narg(after_cutoff)::timestamptz, 'infinity'::timestamptz)
    ORDER BY effective_cutoff DESC
    LIMIT sqlc.arg(scan_limit)::integer
)
SELECT id, effective_cutoff, scheduled_at, committed_at,
       coalesce(CASE WHEN evidence @> '{"Evaluation":{"Maintenance":{"OpeningSuppressed":true}}}'::jsonb
                THEN (evidence #> '{Evaluation,Maintenance}')::text END, '')::text AS suppression
FROM source_batch
ORDER BY effective_cutoff DESC;
