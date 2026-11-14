-- name: UpsertAlertBreachPin :execrows
INSERT INTO public.almanac_alert_breach_pins(occurrence_id,policy_id,revision,evaluation_id)
VALUES($1,$2,$3,$4) ON CONFLICT(occurrence_id) DO UPDATE SET evaluation_id=excluded.evaluation_id
WHERE EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o WHERE o.id=excluded.occurrence_id AND o.state IN ('firing','acknowledged'));

-- name: LockAlertRetentionWork :one
SELECT * FROM public.almanac_alert_retention_work WHERE kind=$1 FOR UPDATE SKIP LOCKED;
-- name: AdvanceAlertRetentionWork :execrows
UPDATE public.almanac_alert_retention_work SET after_policy=$2,after_revision=$3,after_time=$4,after_id=$5,version=version+1 WHERE kind=$1 AND version=$6;

-- name: ScanAlertRetainedEvaluations :many
WITH page AS MATERIALIZED (
 SELECT * FROM public.almanac_alert_evaluations e
 WHERE (e.policy_id,e.revision,e.scheduled_at,e.id)>(sqlc.arg(after_policy)::uuid,sqlc.arg(after_revision)::bigint,sqlc.arg(after_time)::timestamptz,sqlc.arg(after_id)::uuid)
 ORDER BY e.policy_id,e.revision,e.scheduled_at,e.id LIMIT sqlc.arg(batch_limit) FOR UPDATE SKIP LOCKED
)
SELECT e.*,
 EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o WHERE o.firing_evaluation_id=e.id) OR
 EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o WHERE o.latest_evaluation_id=e.id) OR
 EXISTS(SELECT 1 FROM public.almanac_alert_breach_pins p WHERE p.evaluation_id=e.id) OR
 EXISTS(SELECT 1 FROM public.almanac_alert_transitions t WHERE t.evaluation_id=e.id) OR
 EXISTS(SELECT 1 FROM public.almanac_alert_cursors c WHERE c.last_evaluation_id=e.id) OR
 EXISTS(SELECT 1 FROM public.almanac_alert_ranges r WHERE r.reference_evaluation_id=e.id) AS pinned
FROM page e ORDER BY e.policy_id,e.revision,e.scheduled_at,e.id;

-- name: ScanAlertTerminalOccurrences :many
SELECT * FROM public.almanac_alert_occurrences
WHERE state IN ('resolved','disabled','superseded') AND (updated_at,id)>(sqlc.arg(after_time)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY updated_at,id LIMIT sqlc.arg(batch_limit) FOR UPDATE SKIP LOCKED;

-- name: ScanAlertRetentionRanges :many
SELECT * FROM public.almanac_alert_ranges WHERE (recorded_at,id)>(sqlc.arg(after_time)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY recorded_at,id LIMIT sqlc.arg(batch_limit) FOR UPDATE SKIP LOCKED;

-- name: DeleteRetainedAlertTransitions :execrows
DELETE FROM public.almanac_alert_transitions WHERE occurrence_id=ANY($1::uuid[]);
-- name: DeleteRetainedAlertBreachPins :execrows
DELETE FROM public.almanac_alert_breach_pins WHERE occurrence_id=ANY($1::uuid[]);
-- name: DeleteRetainedAlertOccurrences :execrows
DELETE FROM public.almanac_alert_occurrences WHERE id=ANY($1::uuid[]) AND state IN ('resolved','disabled','superseded');
-- name: DeleteRetainedAlertRanges :execrows
DELETE FROM public.almanac_alert_ranges WHERE id=ANY($1::uuid[]);
-- name: DeleteRetainedAlertEvaluations :execrows
DELETE FROM public.almanac_alert_evaluations e WHERE id=ANY($1::uuid[])
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o WHERE o.firing_evaluation_id=e.id)
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o WHERE o.latest_evaluation_id=e.id)
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_breach_pins p WHERE p.evaluation_id=e.id)
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_transitions t WHERE t.evaluation_id=e.id)
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_cursors c WHERE c.last_evaluation_id=e.id)
AND NOT EXISTS(SELECT 1 FROM public.almanac_alert_ranges r WHERE r.reference_evaluation_id=e.id);

-- name: AlertRangesOverlap :one
SELECT EXISTS(SELECT 1 FROM public.almanac_alert_ranges WHERE policy_id=$1 AND revision=$2 AND starts_at<sqlc.arg(ends_at)::timestamptz AND ends_at>sqlc.arg(starts_at)::timestamptz);

-- name: InsertAlertCompactionRange :exec
INSERT INTO public.almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at,summary)
VALUES($1,$2,$3,$4,$5,60,'compacted_evaluations','unchanged_outcome',$6,$7);
