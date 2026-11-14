-- name: PinAlertCutoff :execrows
UPDATE public.almanac_alert_cursors SET pending_cutoff=$3
WHERE policy_id=$1 AND revision=$2 AND pending_cutoff IS NULL AND schedulable
 AND epoch=$4 AND claim_token=$5 AND version=$6 AND next_scheduled_at=$7;
