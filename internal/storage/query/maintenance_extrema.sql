-- name: ReadMaintenanceExtremaClaim :one
SELECT * FROM public.almanac_maintenance_preparation WHERE id=$1;

-- name: ReadMaintenanceExtremaParent :one
SELECT * FROM public.almanac_slo_extrema_cursor
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND calculation_version=$4;

-- name: LockMaintenanceExtremaParent :one
SELECT * FROM public.almanac_slo_extrema_cursor WHERE id=$1 FOR UPDATE;

-- name: PublishMaintenanceSloExtrema :execrows
-- The resource/source, parent cursor and targeted job fences are held by the
-- caller. This publication never advances the parent's progress/dirty range.
INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT w.slo_id,w.definition_revision,w.measurement_revision,sqlc.arg(exclusion_revision),w.calculation_version,3600,
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

-- name: ReadMaintenanceTerminalExtremaTargets :many
-- The caller supplies bounded exact bucket pairs. A terminal target from a
-- different source snapshot cannot block a newly captured repair.
WITH requested AS (
 SELECT unnest(sqlc.arg(starts)::timestamptz[]) AS start_at,
        unnest(sqlc.arg(kinds)::text[]) AS kind
)
SELECT p.kind,p.hour_start FROM requested r
JOIN public.almanac_maintenance_preparation p ON p.kind=r.kind AND p.hour_start=r.start_at
WHERE p.slo_id=sqlc.arg(slo_id) AND p.sli_id=sqlc.arg(sli_id)
 AND p.definition_revision=sqlc.arg(definition_revision)::bigint AND p.measurement_revision=sqlc.arg(measurement_revision)::bigint
 AND p.target_revision=sqlc.arg(target_revision) AND p.source_generation=sqlc.arg(source_generation)::bigint
 AND p.build_id=(SELECT id FROM public.almanac_maintenance_builds WHERE slo_id=sqlc.arg(slo_id) AND target_revision=sqlc.arg(target_revision))
 AND p.state IN ('unavailable','exhausted');
