-- name: EnsureMaintenanceCompatibility :exec
INSERT INTO public.almanac_maintenance_compatibility(slo_id) VALUES($1) ON CONFLICT DO NOTHING;
-- name: LockMaintenanceCompatibility :one
SELECT * FROM public.almanac_maintenance_compatibility WHERE slo_id=$1 FOR UPDATE;
-- name: MarkMaintenanceCompatibilityUnavailable :exec
UPDATE public.almanac_maintenance_compatibility SET unavailable=$2 WHERE slo_id=$1;
-- name: AnalyzeMaintenanceCompatibilityEvent :one
SELECT public.almanac_analyze_maintenance_change(sqlc.arg(slo_id)::uuid,sqlc.arg(revision)::bigint)::boolean AS analyzed;
-- name: ReadMaintenanceCompatibilityAuthority :one
SELECT s.revision AS current_revision, coalesce(c.indexed_through,0)::bigint AS indexed_through,
 coalesce(c.unavailable,false)::boolean AS unavailable, coalesce(c.scope_ordinal,0)::bigint AS scope_ordinal,
 current_setting('transaction_isolation')::text AS isolation
FROM public.almanac_slo_exclusion_state s LEFT JOIN public.almanac_maintenance_compatibility c USING(slo_id) WHERE s.slo_id=$1;
-- name: HasMaintenanceChangedInput :one
SELECT EXISTS(SELECT 1 FROM public.almanac_maintenance_changed_inputs
 WHERE slo_id=sqlc.arg(slo_id)::uuid AND scope_key @> sqlc.arg(scope_ordinal)::bigint
 AND revision_key && int8range(sqlc.arg(after_revision)::bigint+1,CASE WHEN sqlc.arg(through_revision)::bigint=9223372036854775807 THEN NULL ELSE sqlc.arg(through_revision)::bigint+1 END,'[)')
 AND input_range && tstzrange(sqlc.arg(window_start)::timestamptz,sqlc.arg(window_end)::timestamptz,'[)') LIMIT 1)::boolean AS changed;
-- name: CreateMaintenanceComparison :one
INSERT INTO public.almanac_maintenance_comparisons(id,first_build,second_build,created_at,updated_at) VALUES($1,$2,$3,$4,$4)
ON CONFLICT(first_build,second_build) DO UPDATE SET id=almanac_maintenance_comparisons.id RETURNING *;
-- name: LockMaintenanceComparison :one
SELECT * FROM public.almanac_maintenance_comparisons WHERE id=$1 FOR UPDATE;
-- name: ReadMaintenanceUnionPage :many
SELECT starts_at,ends_at FROM public.almanac_maintenance_coverage
WHERE build_id=$1 AND starts_at>coalesce(sqlc.narg(after_start)::timestamptz,'-infinity'::timestamptz)
ORDER BY starts_at LIMIT sqlc.arg(page_limit)::integer;
-- name: AdvanceMaintenanceComparison :exec
UPDATE public.almanac_maintenance_comparisons SET after_start=sqlc.narg(after_start),compared=sqlc.arg(compared),state=sqlc.arg(state),updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);
-- name: ReadMaintenanceEqualComparison :one
SELECT c.id FROM public.almanac_maintenance_comparisons c
JOIN public.almanac_maintenance_builds a ON a.id=c.first_build
JOIN public.almanac_maintenance_builds b ON b.id=c.second_build
WHERE a.slo_id=sqlc.arg(slo_id)::uuid AND b.slo_id=sqlc.arg(slo_id)::uuid
AND a.target_revision=sqlc.arg(first_revision)::bigint AND b.target_revision=sqlc.arg(second_revision)::bigint AND c.state='equal';
