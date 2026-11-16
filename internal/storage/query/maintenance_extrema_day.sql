-- name: PublishMaintenanceSloExtremaDay :execrows
-- The resource/source, parent cursor and targeted job fences are held by the
-- caller. This publication never advances the parent's progress/dirty range.
INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT w.slo_id,w.definition_revision,w.measurement_revision,sqlc.arg(exclusion_revision),w.calculation_version,86400,
 sqlc.arg(range_start)::timestamptz,sqlc.arg(range_end)::timestamptz,sqlc.arg(source_generation),sqlc.arg(retention_start)::timestamptz,sqlc.arg(published_at)::timestamptz,sqlc.arg(lanes)::jsonb
FROM public.almanac_slo_extrema_cursor w
JOIN public.almanac_slos s ON s.id=w.slo_id AND s.definition_revision=w.definition_revision
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision AND d.measurement_revision=w.measurement_revision
JOIN public.almanac_slo_exclusion_state x ON x.slo_id=s.id AND x.revision=sqlc.arg(exclusion_revision)
WHERE w.id=sqlc.arg(id) AND w.version=sqlc.arg(version)
 AND w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision) AND w.measurement_revision=sqlc.arg(measurement_revision)
 AND w.calculation_version=sqlc.arg(calculation_version)
ON CONFLICT(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET range_end=EXCLUDED.range_end,source_generation=EXCLUDED.source_generation,retention_start=EXCLUDED.retention_start,published_at=EXCLUDED.published_at,lanes=EXCLUDED.lanes
WHERE almanac_slo_extrema.source_generation<=EXCLUDED.source_generation;

-- name: ReadMaintenanceTerminalExtremaHours :many
-- Caller supplies at most 24 missing exact hour starts from one day snapshot.
-- An older generation's failure cannot veto a distinct current repair key.
SELECT hour_start FROM public.almanac_maintenance_preparation
WHERE kind='extrema_hour' AND state IN ('unavailable','exhausted')
 AND slo_id=sqlc.arg(slo_id)::uuid AND sli_id=sqlc.arg(sli_id)::uuid
 AND definition_revision=sqlc.arg(definition_revision)::bigint
 AND measurement_revision=sqlc.arg(measurement_revision)::bigint
 AND target_revision=sqlc.arg(target_revision)::bigint
 AND source_generation=sqlc.arg(source_generation)::bigint
 AND build_id=sqlc.arg(build_id)::uuid
 AND hour_start=ANY(sqlc.arg(hour_starts)::timestamptz[])
ORDER BY hour_start LIMIT 24;
