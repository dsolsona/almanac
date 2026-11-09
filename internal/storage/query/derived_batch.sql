-- name: ClaimDerivedDirtyBatch :many
WITH candidates AS (
    SELECT sli_id,measurement_revision,range_start FROM public.almanac_derived_dirty_ranges
    WHERE (attempts<5 OR failing_since IS NULL OR failing_since>sqlc.arg(now)::timestamptz-interval '7 days')
      AND last_error IS DISTINCT FROM 'retention_expired'
      AND next_attempt_at<=sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY range_start DESC,next_attempt_at,sli_id,measurement_revision
    LIMIT sqlc.arg(batch_limit) FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_epoch=d.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz,attempts=LEAST(d.attempts+1,5)
FROM candidates c WHERE d.sli_id=c.sli_id AND d.measurement_revision=c.measurement_revision AND d.range_start=c.range_start
RETURNING d.*;

-- name: RebuildDerivedBatch :many
-- Keep the defensive missing-value result for the caller; canonical row constraints now prevent it.
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,
           unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(generations)::bigint[]) AS generation,
           unnest(sqlc.arg(starts)::timestamptz[]) AS hour_start
), computed AS (
    SELECT r.sli_id,r.revision,r.hour_start,r.generation,a.*
    FROM requested r
    JOIN public.almanac_sli_measurements m ON m.sli_id=r.sli_id AND m.revision=r.revision
    JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
    CROSS JOIN LATERAL (
        SELECT coalesce(sum(c.good_count) FILTER (WHERE c.outcome='observed'),0)::numeric AS good,
               coalesce(sum(c.total_count) FILTER (WHERE c.outcome='observed'),0)::numeric AS total,
               count(*) FILTER (WHERE c.outcome='observed') AS observed,
               count(*) FILTER (WHERE c.outcome='complete_empty') AS known_empty,
               count(*) FILTER (WHERE c.outcome='observed' AND c.total_count=0) AS zero_traffic,
               count(*) FILTER (WHERE c.outcome='observed' AND c.total_count IS NULL) AS missing_values,
               min(c.interval_start) AS first_interval,
               max(c.interval_start+datasource.polling_interval_seconds*interval '1 second') AS last_interval,max(c.recorded_at) AS latest_receipt,
               min(c.interval_start) FILTER (WHERE c.outcome='observed') AS first_observation,
               max(c.interval_start+datasource.polling_interval_seconds*interval '1 second') FILTER (WHERE c.outcome='observed') AS last_observation
        FROM public.almanac_sli_observations c
        WHERE c.sli_id=r.sli_id AND c.measurement_revision=r.revision
          AND c.interval_start>=r.hour_start AND c.interval_start<r.hour_start+interval '1 hour'
    ) a
), stored AS (
    INSERT INTO public.almanac_sli_hourly_aggregates
        (sli_id,measurement_revision,hour_start,source_generation,good,total,
         observed_intervals,known_empty_intervals,zero_traffic_intervals,
         first_interval_start,last_interval_end,latest_receipt_at,first_observation_at,last_observation_at)
    SELECT sli_id,revision,hour_start,generation,good,total,observed,known_empty,zero_traffic,
           first_interval,last_interval,latest_receipt,first_observation,last_observation
    FROM computed
    WHERE first_interval IS NOT NULL AND missing_values=0
    ON CONFLICT (sli_id,measurement_revision,hour_start) DO UPDATE
    SET source_generation=EXCLUDED.source_generation,good=EXCLUDED.good,total=EXCLUDED.total,
        observed_intervals=EXCLUDED.observed_intervals,known_empty_intervals=EXCLUDED.known_empty_intervals,
        zero_traffic_intervals=EXCLUDED.zero_traffic_intervals,first_interval_start=EXCLUDED.first_interval_start,
        last_interval_end=EXCLUDED.last_interval_end,latest_receipt_at=EXCLUDED.latest_receipt_at,
        first_observation_at=EXCLUDED.first_observation_at,last_observation_at=EXCLUDED.last_observation_at
    WHERE almanac_sli_hourly_aggregates.source_generation<=EXCLUDED.source_generation
    RETURNING 1
)
SELECT sli_id::uuid AS sli_id,revision::bigint AS measurement_revision,hour_start::timestamptz AS hour_start
FROM computed WHERE missing_values>0;

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
UPDATE public.almanac_derived_dirty_ranges d SET lease_until=NULL,attempts=0,last_error=NULL,failing_since=NULL
FROM requested r
WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at
  AND d.generation<>r.generation AND d.lease_epoch=r.epoch;

-- name: FailDerivedBatch :exec
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(epochs)::bigint[]) AS epoch,unnest(sqlc.arg(starts)::timestamptz[]) AS start_at
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_until=NULL,next_attempt_at=sqlc.arg(now)::timestamptz+CASE WHEN d.attempts<5 THEN power(2,d.attempts)*interval '1 second' WHEN coalesce(d.failing_since,sqlc.arg(now)::timestamptz)>sqlc.arg(now)::timestamptz-interval '24 hours' THEN interval '5 minutes' ELSE interval '1 hour' END,
    failing_since=coalesce(d.failing_since,sqlc.arg(now)::timestamptz),last_error=sqlc.arg(reason)::text
FROM requested r
WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at AND d.lease_epoch=r.epoch;

-- name: DeferDerivedBatch :exec
-- Hours waiting for their observation values: retry soon without counting an attempt (ADR-0034).
WITH requested AS (
    SELECT unnest(sqlc.arg(sli_ids)::uuid[]) AS sli_id,unnest(sqlc.arg(revisions)::bigint[]) AS revision,
           unnest(sqlc.arg(epochs)::bigint[]) AS epoch,unnest(sqlc.arg(starts)::timestamptz[]) AS start_at
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_until=NULL,attempts=CASE WHEN d.attempts>=5 AND d.failing_since IS NOT NULL THEN d.attempts ELSE GREATEST(d.attempts-1,0) END,next_attempt_at=sqlc.arg(next_attempt_at)::timestamptz,
    last_error=sqlc.arg(reason)::text
FROM requested r
WHERE d.sli_id=r.sli_id AND d.measurement_revision=r.revision AND d.range_start=r.start_at AND d.lease_epoch=r.epoch;
