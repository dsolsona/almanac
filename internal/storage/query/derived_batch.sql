-- name: ClaimDerivedDirtyBatch :many
WITH candidates AS (
    SELECT sli_id,measurement_revision,range_start FROM public.almanac_derived_dirty_ranges
    WHERE attempts<5 AND next_attempt_at<=sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY range_start DESC,next_attempt_at,sli_id,measurement_revision
    LIMIT sqlc.arg(batch_limit) FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_epoch=d.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz,attempts=d.attempts+1
FROM candidates c WHERE d.sli_id=c.sli_id AND d.measurement_revision=c.measurement_revision AND d.range_start=c.range_start
RETURNING d.*;

-- name: RebuildDerivedBatch :exec
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,
           unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(generations)::bigint[]) AS generation,
           unnest(sqlc.arg(starts)::timestamptz[]) AS hour_start
)
INSERT INTO public.almanac_sli_hourly_aggregates
    (sli_id,measurement_revision,hour_start,source_generation,good,total,
     observed_intervals,known_empty_intervals,zero_traffic_intervals,
     first_interval_start,last_interval_end,latest_receipt_at,first_observation_at,last_observation_at)
SELECT r.sli_id,r.revision,r.hour_start,r.generation,
       a.good,a.total,a.observed,a.known_empty,a.zero_traffic,
       a.first_interval,a.last_interval,a.latest_receipt,a.first_observation,a.last_observation
FROM requested r CROSS JOIN LATERAL (
    SELECT coalesce(sum(v.good_count) FILTER (WHERE c.outcome='observed'),0)::numeric AS good,
           coalesce(sum(v.total_count) FILTER (WHERE c.outcome='observed'),0)::numeric AS total,
           count(*) FILTER (WHERE c.outcome='observed') AS observed,
           count(*) FILTER (WHERE c.outcome='complete_empty') AS known_empty,
           count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0) AS zero_traffic,
           min(c.interval_start) AS first_interval,max(c.interval_end) AS last_interval,max(c.recorded_at) AS latest_receipt,
           min(c.interval_start) FILTER (WHERE c.outcome='observed') AS first_observation,
           max(c.interval_end) FILTER (WHERE c.outcome='observed') AS last_observation
    FROM public.almanac_sli_coverage c
    LEFT JOIN public.almanac_sli_observation_values v
      ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision
    WHERE c.sli_id=r.sli_id AND c.measurement_revision=r.revision
      AND c.interval_start>=r.hour_start AND c.interval_start<r.hour_start+interval '1 hour'
) a
WHERE a.first_interval IS NOT NULL
ON CONFLICT (sli_id,measurement_revision,hour_start) DO UPDATE
SET source_generation=EXCLUDED.source_generation,good=EXCLUDED.good,total=EXCLUDED.total,
    observed_intervals=EXCLUDED.observed_intervals,known_empty_intervals=EXCLUDED.known_empty_intervals,
    zero_traffic_intervals=EXCLUDED.zero_traffic_intervals,first_interval_start=EXCLUDED.first_interval_start,
    last_interval_end=EXCLUDED.last_interval_end,latest_receipt_at=EXCLUDED.latest_receipt_at,
    first_observation_at=EXCLUDED.first_observation_at,last_observation_at=EXCLUDED.last_observation_at
WHERE almanac_sli_hourly_aggregates.source_generation<=EXCLUDED.source_generation;

-- name: AcknowledgeDerivedBatch :exec
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(generations)::bigint[]) AS generation,unnest(sqlc.arg(epochs)::bigint[]) AS epoch,
           unnest(sqlc.arg(starts)::timestamptz[]) AS start_at
), removed AS (
    DELETE FROM public.almanac_derived_dirty_ranges d USING requested r
    WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at
      AND d.generation=r.generation AND d.lease_epoch=r.epoch RETURNING d.sli_id
)
UPDATE public.almanac_derived_dirty_ranges d SET lease_until=NULL,attempts=0,last_error=NULL
FROM requested r
WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at
  AND d.generation<>r.generation AND d.lease_epoch=r.epoch;

-- name: FailDerivedBatch :exec
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(epochs)::bigint[]) AS epoch,unnest(sqlc.arg(starts)::timestamptz[]) AS start_at
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_until=NULL,next_attempt_at=sqlc.arg(now)::timestamptz+power(2,LEAST(d.attempts,5))*interval '1 second',last_error='aggregate_rebuild_failed'
FROM requested r
WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at AND d.lease_epoch=r.epoch;
