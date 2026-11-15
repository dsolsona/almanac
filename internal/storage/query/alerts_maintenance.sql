-- Source exhaustion is independent of the maintenance mask. Read only the
-- bounded window/interior identities, never counts from an exhausted hour.
-- name: ReadAlertMaintenanceSourceFailures :many
SELECT x.ordinal::integer AS ordinal, EXISTS(
 SELECT 1 FROM public.almanac_derived_dirty_ranges d
 WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
 AND x.starts_at<(sqlc.arg(ends)::timestamptz[])[x.ordinal]
 AND d.range_start<(sqlc.arg(ends)::timestamptz[])[x.ordinal] AND d.range_end>x.starts_at
 AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
) AS exhausted
FROM unnest(sqlc.arg(starts)::timestamptz[]) WITH ORDINALITY x(starts_at,ordinal)
ORDER BY x.ordinal;

-- The enqueue phase already holds these exact queue rows. Inspect their latest
-- state after an upsert wait without acquiring another resource lock.
-- name: ReadAlertMaintenancePreparationState :one
SELECT state FROM public.almanac_maintenance_preparation
WHERE kind=sqlc.arg(kind) AND slo_id=sqlc.arg(slo_id)
 AND target_revision=sqlc.arg(target_revision) AND build_id=sqlc.arg(build_id)
 AND second_build IS NOT DISTINCT FROM sqlc.narg(second_build)::uuid
 AND sli_id IS NOT DISTINCT FROM sqlc.narg(sli_id)::uuid
 AND definition_revision IS NOT DISTINCT FROM sqlc.narg(definition_revision)::bigint
 AND measurement_revision IS NOT DISTINCT FROM sqlc.narg(measurement_revision)::bigint
 AND hour_start IS NOT DISTINCT FROM sqlc.narg(hour_start)::timestamptz
 AND source_generation IS NOT DISTINCT FROM sqlc.narg(source_generation)::bigint;
