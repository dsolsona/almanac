-- name: ListLegacyReceiptHours :many
SELECT sli_id,measurement_revision,hour_start
FROM public.almanac_sli_hourly_aggregates
WHERE receipt_source_generation IS DISTINCT FROM source_generation
   OR certified_receipt_at IS DISTINCT FROM latest_receipt_at
ORDER BY hour_start DESC,sli_id,measurement_revision
LIMIT sqlc.arg(page_limit)::integer;

-- name: LockCanonicalReceiptSli :one
SELECT id FROM public.almanac_slis WHERE id=$1 FOR SHARE;

-- name: LockLegacyReceiptHour :one
SELECT source_generation,good::text AS good,total::text AS total,
       observed_intervals,known_empty_intervals,zero_traffic_intervals,
       first_interval_start,last_interval_end,first_observation_at,last_observation_at,latest_receipt_at,
       NOT EXISTS (
         SELECT 1 FROM public.almanac_derived_dirty_ranges d
         WHERE d.sli_id=h.sli_id AND d.measurement_revision=h.measurement_revision
           AND d.range_start<h.hour_start+interval '1 hour' AND d.range_end>h.hour_start
       ) AS population_ready
FROM public.almanac_sli_hourly_aggregates h
WHERE h.sli_id=sqlc.arg(sli_id) AND h.measurement_revision=sqlc.arg(measurement_revision)
  AND h.hour_start=sqlc.arg(hour_start)
  AND (receipt_source_generation IS DISTINCT FROM source_generation
       OR certified_receipt_at IS DISTINCT FROM latest_receipt_at)
FOR UPDATE SKIP LOCKED;

-- name: ReadCanonicalReceiptPopulation :one
-- Read at most one physical hour plus one overflow sentinel. No count or source
-- generation is repaired from this input; callers compare the exact retained
-- population to the locked stored summary before certifying receipt metadata.
WITH population AS MATERIALIZED (
 SELECT c.interval_start,c.interval_start+d.polling_interval_seconds*interval '1 second' AS interval_end,
        c.outcome,c.good_count,c.total_count,c.recorded_at
 FROM public.almanac_sli_observations c
 JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
 JOIN public.almanac_datasources d ON d.id=m.datasource_id
 WHERE c.sli_id=sqlc.arg(sli_id) AND c.measurement_revision=sqlc.arg(measurement_revision)
   AND c.interval_start>=sqlc.arg(hour_start)::timestamptz
   AND c.interval_start<sqlc.arg(hour_start)::timestamptz+interval '1 hour'
 ORDER BY c.interval_start LIMIT 3601
)
SELECT count(*)::bigint AS raw_rows,
       coalesce(sum(good_count) FILTER (WHERE outcome='observed'),0)::numeric::text AS good,
       coalesce(sum(total_count) FILTER (WHERE outcome='observed'),0)::numeric::text AS total,
       count(*) FILTER (WHERE outcome='observed')::bigint AS observed,
       count(*) FILTER (WHERE outcome='complete_empty')::bigint AS known_empty,
       count(*) FILTER (WHERE outcome='observed' AND total_count=0)::bigint AS zero_traffic,
       min(interval_start)::timestamptz AS first_interval,
       max(interval_end)::timestamptz AS last_interval,
       min(interval_start) FILTER (WHERE outcome='observed')::timestamptz AS first_observation,
       max(interval_end) FILTER (WHERE outcome='observed')::timestamptz AS last_observation,
       max(recorded_at) FILTER (WHERE outcome IN ('observed','complete_empty'))::timestamptz AS latest_receipt
FROM population;

-- name: UpdateCanonicalReceiptMetadata :execrows
UPDATE public.almanac_sli_hourly_aggregates
SET latest_receipt_at=sqlc.narg(latest_receipt_at)::timestamptz,
    receipt_source_generation=source_generation,
    certified_receipt_at=sqlc.narg(latest_receipt_at)::timestamptz
WHERE sli_id=sqlc.arg(sli_id) AND measurement_revision=sqlc.arg(measurement_revision)
  AND hour_start=sqlc.arg(hour_start) AND source_generation=sqlc.arg(source_generation)
  AND (receipt_source_generation IS DISTINCT FROM source_generation
       OR certified_receipt_at IS DISTINCT FROM latest_receipt_at);
