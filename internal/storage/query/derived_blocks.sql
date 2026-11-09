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
SET state=CASE WHEN attempts>=5 AND failing_since IS NOT NULL THEN 'failed' ELSE 'pending' END,
    attempts=CASE WHEN attempts>=5 AND failing_since IS NOT NULL THEN attempts ELSE GREATEST(attempts-1,0) END, lease_until=NULL, next_attempt_at=$3
WHERE id=$1 AND lease_epoch=$2 AND state='running';
