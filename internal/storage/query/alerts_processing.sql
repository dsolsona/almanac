-- name: ReadAlertCursor :one
SELECT * FROM public.almanac_alert_cursors WHERE policy_id=$1 AND revision=$2;

-- name: FailAlertCursor :execrows
UPDATE public.almanac_alert_cursors SET failure_attempts=$3,
 failure_since=coalesce(failure_since,sqlc.arg(now)::timestamptz),last_failure_at=sqlc.arg(now),
 exhausted=$4,available_at=$5,waiting_reason=$6,claim_token=NULL,claim_until=NULL
WHERE policy_id=$1 AND revision=$2 AND epoch=$7 AND claim_token=$8
 AND version=$9 AND next_scheduled_at=$10 AND schedulable AND NOT exhausted;

-- name: RetryAlertCursor :execrows
UPDATE public.almanac_alert_cursors SET failure_attempts=0,failure_since=NULL,
 last_failure_at=NULL,exhausted=false,available_at=$3,waiting_reason='',
 version=version+1,epoch=epoch+1,claim_token=NULL,claim_until=NULL
WHERE policy_id=$1 AND revision=$2 AND version=$4 AND schedulable AND exhausted;
