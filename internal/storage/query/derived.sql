-- name: ClaimDerivedDirtyRange :one
WITH candidate AS (
    SELECT sli_id, measurement_revision, range_start
    FROM public.almanac_derived_dirty_ranges
    WHERE next_attempt_at <= sqlc.arg(now)::timestamptz AND attempts < 5
      AND (lease_until IS NULL OR lease_until <= sqlc.arg(now)::timestamptz)
    ORDER BY next_attempt_at, range_start DESC, sli_id, measurement_revision
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_derived_dirty_ranges d
SET lease_epoch=d.lease_epoch+1, lease_until=sqlc.arg(lease_until)::timestamptz,
    attempts=d.attempts+1
FROM candidate c
WHERE d.sli_id=c.sli_id AND d.measurement_revision=c.measurement_revision AND d.range_start=c.range_start
RETURNING d.*;

-- name: RebuildDerivedHours :exec
INSERT INTO public.almanac_sli_hourly_aggregates
    (sli_id, measurement_revision, hour_start, source_generation, good, total,
     observed_intervals, known_empty_intervals, zero_traffic_intervals,
     first_interval_start, last_interval_end, latest_receipt_at, first_observation_at, last_observation_at)
SELECT c.sli_id, c.measurement_revision,
       date_trunc('hour', c.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
       sqlc.arg(source_generation)::bigint,
       coalesce(sum(v.good_count) FILTER (WHERE c.outcome='observed'), 0)::numeric,
       coalesce(sum(v.total_count) FILTER (WHERE c.outcome='observed'), 0)::numeric,
       count(*) FILTER (WHERE c.outcome='observed'),
       count(*) FILTER (WHERE c.outcome='complete_empty'),
       count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0),
       min(c.interval_start), max(c.interval_end), max(c.recorded_at),
       min(c.interval_start) FILTER (WHERE c.outcome='observed'),
       max(c.interval_end) FILTER (WHERE c.outcome='observed')
FROM public.almanac_sli_coverage c
LEFT JOIN public.almanac_sli_observation_values v
    ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id
    AND v.measurement_revision=c.measurement_revision
WHERE c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
  AND c.interval_start >= sqlc.arg(range_start)::timestamptz
  AND c.interval_start < sqlc.arg(range_end)::timestamptz
GROUP BY c.sli_id, c.measurement_revision,
         date_trunc('hour', c.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
ON CONFLICT (sli_id, measurement_revision, hour_start) DO UPDATE
SET source_generation=EXCLUDED.source_generation, good=EXCLUDED.good, total=EXCLUDED.total,
    observed_intervals=EXCLUDED.observed_intervals,
    known_empty_intervals=EXCLUDED.known_empty_intervals,
    zero_traffic_intervals=EXCLUDED.zero_traffic_intervals,
    first_interval_start=EXCLUDED.first_interval_start,
    last_interval_end=EXCLUDED.last_interval_end, latest_receipt_at=EXCLUDED.latest_receipt_at,
    first_observation_at=EXCLUDED.first_observation_at, last_observation_at=EXCLUDED.last_observation_at
WHERE almanac_sli_hourly_aggregates.source_generation <= EXCLUDED.source_generation;

-- name: AcknowledgeDerivedDirtyRange :execrows
DELETE FROM public.almanac_derived_dirty_ranges
WHERE sli_id=$1 AND measurement_revision=$2 AND generation=$3 AND lease_epoch=$4 AND range_start=sqlc.arg(work_start);

-- name: ReleaseDerivedDirtyRange :execrows
UPDATE public.almanac_derived_dirty_ranges
SET lease_until=NULL, attempts=0, last_error=NULL
WHERE sli_id=$1 AND measurement_revision=$2 AND lease_epoch=$3 AND range_start=sqlc.arg(work_start);

-- name: FailDerivedDirtyRange :execrows
UPDATE public.almanac_derived_dirty_ranges
SET lease_until=NULL, next_attempt_at=$4, last_error=$5
WHERE sli_id=$1 AND measurement_revision=$2 AND lease_epoch=$3 AND range_start=sqlc.arg(work_start);

-- name: GetDerivedSourceGeneration :one
SELECT generation FROM public.almanac_sli_source_generations
WHERE sli_id=$1 AND measurement_revision=$2;

-- name: RenewDerivedDirtyLease :execrows
UPDATE public.almanac_derived_dirty_ranges SET lease_until=$5
WHERE sli_id=$1 AND measurement_revision=$2 AND generation=$3 AND lease_epoch=$4
  AND range_start=sqlc.arg(work_start);

-- name: ListDerivedHourlyCounts :many
SELECT hour_start, good::text AS good, total::text AS total,
       observed_intervals, known_empty_intervals, zero_traffic_intervals,
       first_interval_start, last_interval_end, latest_receipt_at
FROM public.almanac_sli_hourly_aggregates
WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start >= $3 AND hour_start < $4
ORDER BY hour_start LIMIT $5;

-- name: ReadDerivedWindowTotals :one
WITH readiness AS (
    SELECT NOT EXISTS (
        SELECT 1 FROM public.almanac_derived_dirty_ranges d
        WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
          AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
          AND d.range_start<sqlc.arg(full_end)::timestamptz
          AND d.range_end>sqlc.arg(full_start)::timestamptz
    ) AS prepared,EXISTS (
        SELECT 1 FROM public.almanac_derived_dirty_ranges d
        WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
          AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
          AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
          AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
    ) AS exhausted
), raw_ranges AS (
    SELECT unnest(sqlc.arg(edge_starts)::timestamptz[]) AS start_at,
           unnest(sqlc.arg(edge_ends)::timestamptz[]) AS end_at
), contributions AS (
    SELECT a.good, a.total, a.observed_intervals AS observed,
           a.known_empty_intervals AS known_empty, a.zero_traffic_intervals AS zero_traffic,
           a.first_observation_at AS first_observation, a.last_observation_at AS last_observation,
           a.latest_receipt_at AS latest_receipt
    FROM public.almanac_sli_hourly_aggregates a
    WHERE a.sli_id=sqlc.arg(sli_id) AND a.measurement_revision=sqlc.arg(measurement_revision)
      AND a.hour_start >= sqlc.arg(full_start)::timestamptz
      AND a.hour_start < sqlc.arg(full_end)::timestamptz
      AND (SELECT prepared FROM readiness)
    UNION ALL
    SELECT coalesce(sum(v.good_count),0)::numeric, coalesce(sum(v.total_count),0)::numeric,
           count(*) FILTER (WHERE c.outcome='observed'),
           count(*) FILTER (WHERE c.outcome='complete_empty'),
           count(*) FILTER (WHERE c.outcome='observed' AND v.total_count=0),
           min(c.interval_start) FILTER (WHERE c.outcome='observed'),
           max(c.interval_end) FILTER (WHERE c.outcome='observed'), max(c.recorded_at)
    FROM raw_ranges r JOIN public.almanac_sli_coverage c
      ON c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
      AND c.interval_start >= r.start_at AND c.interval_start < r.end_at
      AND c.interval_end <= sqlc.arg(window_end)::timestamptz
    LEFT JOIN public.almanac_sli_observation_values v
      ON v.interval_start=c.interval_start AND v.sli_id=c.sli_id
      AND v.measurement_revision=c.measurement_revision
    WHERE (SELECT prepared FROM readiness)
)
SELECT coalesce(sum(good),0)::numeric::text AS good,
       coalesce(sum(total),0)::numeric::text AS total,
       coalesce(sum(observed),0)::bigint AS observed,
       coalesce(sum(known_empty),0)::bigint AS known_empty,
       coalesce(sum(zero_traffic),0)::bigint AS zero_traffic,
       min(first_observation)::timestamptz AS first_observation,
       max(last_observation)::timestamptz AS last_observation,
       max(latest_receipt)::timestamptz AS latest_receipt,
       (SELECT prepared FROM readiness)::boolean AS prepared,
       (SELECT exhausted FROM readiness)::boolean AS exhausted
FROM contributions;

-- name: HasExhaustedDerivedSource :one
SELECT EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
      AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
      AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
);
