-- Internal backend journal; not a public or remote-worker API.
-- name: InsertAlertPolicy :exec
INSERT INTO public.almanac_alert_policies(id,name,slo_id,evaluation_revision,severity,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6);
-- name: InsertAlertPolicyRevision :exec
INSERT INTO public.almanac_alert_policy_revisions(policy_id,revision,slo_id,definition_revision,calculation_version,conditions,created_at) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: InsertAlertCursor :exec
INSERT INTO public.almanac_alert_cursors(policy_id,revision,origin_at,next_scheduled_at,available_at) VALUES($1,$2,$3,$3,$3);
-- name: ClaimAlertCursors :many
WITH candidates AS (
 SELECT c.policy_id,c.revision FROM public.almanac_alert_cursors c JOIN public.almanac_alert_policies p ON p.id=c.policy_id
 WHERE c.schedulable AND p.enabled AND p.evaluation_revision=c.revision
 AND c.next_scheduled_at<=sqlc.arg(now)::timestamptz AND c.available_at<=sqlc.arg(now)::timestamptz
 AND (c.last_attempt_tick IS NULL OR c.last_attempt_tick<sqlc.arg(tick)::timestamptz)
 AND (c.claim_until IS NULL OR c.claim_until<=sqlc.arg(now)::timestamptz)
 ORDER BY c.last_dispatched_at NULLS FIRST,c.next_scheduled_at,c.policy_id
 LIMIT sqlc.arg(batch_limit) FOR UPDATE OF c SKIP LOCKED
)
UPDATE public.almanac_alert_cursors c SET epoch=c.epoch+1,claim_token=sqlc.arg(token),claim_until=sqlc.arg(until),last_attempt_tick=sqlc.arg(tick),last_dispatched_at=sqlc.arg(now),waiting_reason=''
FROM candidates selected WHERE (c.policy_id,c.revision)=(selected.policy_id,selected.revision) RETURNING c.*;
-- name: ReadAlertBinding :one
SELECT p.id,p.slo_id,p.evaluation_revision,r.definition_revision,r.calculation_version,r.conditions,d.sli_id,d.measurement_revision
FROM public.almanac_alert_policies p JOIN public.almanac_alert_policy_revisions r ON r.policy_id=p.id AND r.revision=p.evaluation_revision
JOIN public.almanac_slo_definitions d ON d.slo_id=r.slo_id AND d.revision=r.definition_revision WHERE p.id=$1;
-- name: LockAlertSli :one
SELECT measurement_revision FROM public.almanac_slis WHERE id=$1 FOR SHARE;
-- name: LockAlertSlo :one
SELECT definition_revision FROM public.almanac_slos WHERE id=$1 FOR SHARE;
-- name: FenceAlertSourceGeneration :one
SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=$2 FOR SHARE;
-- name: LockAlertPolicy :one
SELECT * FROM public.almanac_alert_policies WHERE id=$1 FOR UPDATE;
-- name: LockAlertCursor :one
SELECT * FROM public.almanac_alert_cursors WHERE policy_id=$1 AND revision=$2 FOR UPDATE;
-- name: FindAlertEvaluationReceipt :one
SELECT * FROM public.almanac_alert_evaluations WHERE policy_id=$1 AND revision=$2 AND scheduled_at=$3;
-- name: LockAlertOccurrence :one
SELECT * FROM public.almanac_alert_occurrences WHERE id=$1 FOR UPDATE;
-- name: InsertAlertEvaluation :exec
INSERT INTO public.almanac_alert_evaluations(id,policy_id,revision,scheduled_at,effective_cutoff,truth,source_generation,exclusion_revision,claim_epoch,claim_token,input_digest,evidence,result,committed_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14);
-- name: InsertAlertOccurrence :exec
INSERT INTO public.almanac_alert_occurrences(id,policy_id,revision,state,version,transition_sequence,firing_evaluation_id,latest_evaluation_id,payload,policy_snapshot,opened_at,updated_at,terminal_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);
-- name: UpdateAlertOccurrence :execrows
UPDATE public.almanac_alert_occurrences SET state=$2,version=version+1,transition_sequence=$3,latest_evaluation_id=$4,payload=$5,updated_at=$6,terminal_at=$7
WHERE id=$1 AND version=$8 AND state IN ('firing','acknowledged');
-- name: InsertAlertTransition :exec
INSERT INTO public.almanac_alert_transitions(id,policy_id,revision,occurrence_id,sequence,from_state,to_state,cause,effective_at,detected_at,actor_id,reason,evaluation_id,request_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14);
-- name: AdvanceAlertCursor :execrows
UPDATE public.almanac_alert_cursors SET next_scheduled_at=$3,available_at=$3,version=version+1,last_cutoff=$4,last_evaluation_id=$5,open_occurrence_id=$6,pending_cutoff=NULL,claim_token=NULL,claim_until=NULL,waiting_reason=''
WHERE policy_id=$1 AND revision=$2 AND epoch=$7 AND claim_token=$8 AND version=$9 AND next_scheduled_at=sqlc.arg(expected_boundary) AND schedulable;
-- name: DeferAlertCursor :execrows
UPDATE public.almanac_alert_cursors SET available_at=$3,claim_token=NULL,claim_until=NULL,waiting_reason=$4
WHERE policy_id=$1 AND revision=$2 AND epoch=$5 AND claim_token=$6 AND version=$7 AND next_scheduled_at=$8 AND claim_until>$9 AND schedulable;
-- name: InsertAlertRange :exec
INSERT INTO public.almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at,reference_evaluation_id,actor_id,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);
-- name: DisableAlertPolicyIdentity :execrows
UPDATE public.almanac_alert_policies SET enabled=false,version=version+1,updated_at=$3 WHERE id=$1 AND version=$2 AND enabled;
-- name: CancelAlertCursor :execrows
UPDATE public.almanac_alert_cursors SET schedulable=false,next_scheduled_at=$3,available_at=$3,version=version+1,epoch=epoch+1,pending_cutoff=NULL,claim_token=NULL,claim_until=NULL,open_occurrence_id=NULL,waiting_reason='policy_disabled'
WHERE policy_id=$1 AND revision=$2 AND version=$4;
