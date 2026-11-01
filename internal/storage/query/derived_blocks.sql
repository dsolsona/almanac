-- name: ReadReusableSloBlocks :many
WITH requested AS (
    SELECT unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,
           unnest(sqlc.arg(ends)::timestamptz[]) AS end_at,
           unnest(sqlc.arg(include_starts)::boolean[]) AS include_start
)
SELECT b.range_start,b.range_end,b.payload
FROM requested r
JOIN public.almanac_slo_display_blocks b
  ON b.slo_id=sqlc.arg(slo_id) AND b.definition_revision=sqlc.arg(definition_revision)
 AND b.measurement_revision=sqlc.arg(measurement_revision) AND b.calculation_version=sqlc.arg(calculation_version)
 AND b.lane=sqlc.arg(lane) AND b.window_seconds=sqlc.arg(window_seconds)
 AND b.freshness_grace_us=sqlc.arg(freshness_grace_us)
 AND b.range_start=r.start_at AND b.range_end=r.end_at AND b.include_start=r.include_start
WHERE (b.retention_start=sqlc.arg(retention_start)::timestamptz
    OR b.range_start-b.window_seconds*interval '1 second'>=GREATEST(b.retention_start,sqlc.arg(retention_start)::timestamptz)
    OR b.range_end<=LEAST(b.retention_start,sqlc.arg(retention_start)::timestamptz))
  AND b.payload_digest=sha256(convert_to(b.payload::text,'UTF8'))
  AND NOT EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=b.measurement_revision
      AND d.generation>b.source_generation
      AND d.range_start>=(date_trunc('hour',(b.range_start-b.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND d.range_start<b.range_end AND d.range_end>b.range_start-b.window_seconds*interval '1 second'
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.almanac_sli_hourly_aggregates a
    WHERE a.sli_id=sqlc.arg(sli_id) AND a.measurement_revision=b.measurement_revision
      AND a.source_generation>b.source_generation
      AND a.hour_start>=(date_trunc('hour',(b.range_start-b.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND a.hour_start<b.range_end AND a.hour_start+interval '1 hour'>b.range_start-b.window_seconds*interval '1 second'
  );

-- name: UpsertSloDisplayBlock :exec
INSERT INTO public.almanac_slo_display_blocks
    (slo_id,definition_revision,measurement_revision,calculation_version,lane,window_seconds,
     freshness_grace_us,range_start,range_end,include_start,source_generation,retention_start,payload,updated_at,payload_digest)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,sha256(convert_to($13::jsonb::text,'UTF8')))
ON CONFLICT (slo_id,definition_revision,measurement_revision,calculation_version,lane,window_seconds,
             freshness_grace_us,range_start,range_end,include_start)
DO UPDATE SET source_generation=EXCLUDED.source_generation,retention_start=EXCLUDED.retention_start,
              payload=EXCLUDED.payload,updated_at=EXCLUDED.updated_at,payload_digest=EXCLUDED.payload_digest
WHERE (almanac_slo_display_blocks.source_generation,almanac_slo_display_blocks.retention_start)
   <= (EXCLUDED.source_generation,EXCLUDED.retention_start);

-- name: DeleteExpiredSloDisplayBlocks :execrows
DELETE FROM public.almanac_slo_display_blocks b
WHERE (b.slo_id,b.definition_revision,b.measurement_revision,b.calculation_version,b.lane,b.window_seconds,
       b.freshness_grace_us,b.range_start,b.range_end,b.include_start) IN (
    SELECT old.slo_id,old.definition_revision,old.measurement_revision,old.calculation_version,old.lane,old.window_seconds,
           old.freshness_grace_us,old.range_start,old.range_end,old.include_start
    FROM public.almanac_slo_display_blocks old WHERE old.updated_at<$1
    ORDER BY old.updated_at LIMIT 100
);

-- name: ReleaseSloHistoryForDependencies :execrows
UPDATE public.almanac_slo_history_jobs
SET state='pending', attempts=GREATEST(attempts-1,0), lease_until=NULL, next_attempt_at=$3
WHERE id=$1 AND lease_epoch=$2 AND state='running';
