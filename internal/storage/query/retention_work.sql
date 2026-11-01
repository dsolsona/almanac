-- name: InsertRetentionPreview :execrows
INSERT INTO public.almanac_retention_work
    (id,scope_digest,range_start,range_end,cutoff,preview,previewed_at,next_attempt_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$7::timestamptz+interval '1 minute')
ON CONFLICT (scope_digest) DO NOTHING;

-- name: ExhaustRetentionWork :exec
UPDATE public.almanac_retention_work SET state='failed',lease_until=NULL,last_error='retention_cleanup_failed'
WHERE state IN ('previewed','blocked') AND attempts>=5 AND next_attempt_at<=$1
  AND (lease_until IS NULL OR lease_until<=$1);

-- name: ClaimRetentionWork :one
WITH candidate AS (
    SELECT id FROM public.almanac_retention_work WHERE state IN ('previewed','blocked') AND attempts<5
      AND next_attempt_at<=sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_retention_work w
SET attempts=w.attempts+1,lease_epoch=w.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz
FROM candidate c WHERE w.id=c.id RETURNING w.*;

-- name: LockRetentionWork :one
SELECT id FROM public.almanac_retention_work
WHERE id=$1 AND lease_epoch=$2 AND state IN ('previewed','blocked') FOR UPDATE;

-- name: RetentionHasDependencies :one
SELECT coalesce(EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d WHERE d.range_start<sqlc.arg(range_end)::timestamptz
      AND d.range_end>sqlc.arg(range_start)::timestamptz
) OR EXISTS (
    SELECT 1 FROM public.almanac_slo_history_jobs j WHERE j.state<>'failed'
      AND j.retention_start<sqlc.arg(range_end)::timestamptz
      AND greatest(j.range_end,j.evaluated_through)>sqlc.arg(range_start)::timestamptz
) OR EXISTS (
    SELECT 1 FROM public.almanac_slo_series_repair r
    WHERE r.cursor_at<sqlc.arg(range_end)::timestamptz AND r.end_at>sqlc.arg(range_start)::timestamptz
      AND r.lease_until>sqlc.arg(now)::timestamptz
),false)::boolean AS blocked;

-- name: ListRetentionScopes :many
SELECT scope_digest FROM public.almanac_retention_work ORDER BY range_start,id LIMIT 1000;

-- name: CompleteRetentionWork :execrows
UPDATE public.almanac_retention_work SET state='applied',applied_at=$3,lease_until=NULL,last_error=NULL
WHERE id=$1 AND lease_epoch=$2 AND state IN ('previewed','blocked');

-- name: DeferRetentionWork :exec
UPDATE public.almanac_retention_work SET state='blocked',lease_until=NULL,attempts=GREATEST(attempts-1,0),
    next_attempt_at=$3::timestamptz+interval '30 seconds',last_error='retention_waiting_for_work'
WHERE id=$1 AND lease_epoch=$2 AND state IN ('previewed','blocked');

-- name: FailRetentionWork :exec
UPDATE public.almanac_retention_work SET state=CASE WHEN attempts>=5 OR sqlc.arg(obsolete)::boolean THEN 'failed' ELSE 'previewed' END,
    lease_until=NULL,next_attempt_at=sqlc.arg(now)::timestamptz+interval '30 seconds',last_error=sqlc.arg(last_error)::text
WHERE id=$1 AND lease_epoch=$2 AND state IN ('previewed','blocked');
