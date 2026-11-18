-- name: ObserveServiceMaintenanceAuthority :one
SELECT revision FROM public.almanac_service_maintenance_authority WHERE singleton;
-- name: FenceServiceMaintenanceAuthority :one
SELECT revision FROM public.almanac_service_maintenance_authority WHERE singleton FOR SHARE;
-- name: AdvanceServiceMaintenanceAuthority :one
UPDATE public.almanac_service_maintenance_authority SET revision = revision + 1
WHERE singleton RETURNING revision;

-- name: InsertMaintenanceServiceSelector :exec
INSERT INTO public.almanac_maintenance_service_selectors(exclusion_id, revision, service_id)
VALUES ($1, $2, $3);
-- name: ListMaintenanceServiceSelectors :many
SELECT service_id FROM public.almanac_maintenance_service_selectors
WHERE exclusion_id = $1 AND revision = $2 ORDER BY service_id;
-- name: EnqueueServiceMaintenanceProjection :exec
INSERT INTO public.almanac_service_maintenance_projection(exclusion_id, desired_revision) VALUES ($1, $2)
ON CONFLICT (exclusion_id) DO UPDATE
SET desired_revision = excluded.desired_revision, through_slo = DEFAULT, complete = DEFAULT;

-- name: ListServiceMaintenanceCadences :many
SELECT DISTINCT d.cadence_seconds
FROM public.almanac_slo_definitions d
JOIN public.almanac_slos slo ON slo.id = d.slo_id AND slo.definition_revision = d.revision
JOIN public.almanac_services s ON s.id = d.service_id
WHERE s.archived_at IS NULL
  AND (sqlc.arg(all_services)::boolean OR d.service_id = ANY(sqlc.arg(service_ids)::uuid[]))
ORDER BY d.cadence_seconds;

-- A witness is sufficient to suppress an occurrence. Look up the membership
-- and immutable selector revision effective at the evaluation cutoff. Projection
-- state cannot participate in this decision.
-- name: ReadMaintenanceSuppressionWitness :one
WITH assignment AS MATERIALIZED (
    SELECT a.service_id, a.effective_at, s.archived_at, (
        SELECT subsequent.effective_at FROM public.almanac_slo_service_assignments subsequent
        WHERE subsequent.slo_id = a.slo_id
          AND (subsequent.effective_at, subsequent.definition_revision) > (a.effective_at, a.definition_revision)
        ORDER BY subsequent.effective_at, subsequent.definition_revision LIMIT 1
    ) AS joined_other_service_at
    FROM public.almanac_slo_service_assignments a
    JOIN public.almanac_services s ON s.id = a.service_id
    WHERE a.slo_id = sqlc.arg(slo_id) AND a.effective_at <= sqlc.arg(cutoff)::timestamptz
    ORDER BY a.effective_at DESC, a.definition_revision DESC LIMIT 1
), candidates AS (
    SELECT r.exclusion_id, r.revision, r.starts_at, r.ends_at, r.recorded_at
    FROM public.almanac_maintenance_exclusion_scopes selected
    JOIN public.almanac_maintenance_exclusion_revisions r
      ON r.exclusion_id = selected.exclusion_id AND r.revision = selected.revision
    WHERE selected.slo_id = sqlc.arg(slo_id) AND r.scope_kind = 'slos' AND NOT r.tombstone
      AND tstzrange(r.starts_at, r.ends_at, '[)') @> sqlc.arg(cutoff)::timestamptz
    UNION ALL
    SELECT r.exclusion_id, r.revision, GREATEST(r.starts_at, a.effective_at), LEAST(r.ends_at, a.archived_at, a.joined_other_service_at), r.recorded_at
    FROM assignment a
    JOIN public.almanac_maintenance_service_selectors selected ON selected.service_id = a.service_id
    JOIN public.almanac_maintenance_exclusion_revisions r
      ON r.exclusion_id = selected.exclusion_id AND r.revision = selected.revision
    WHERE (a.archived_at IS NULL OR a.archived_at > sqlc.arg(cutoff)::timestamptz)
      AND r.scope_kind = 'services' AND NOT r.tombstone
      AND tstzrange(r.starts_at, r.ends_at, '[)') @> sqlc.arg(cutoff)::timestamptz
    UNION ALL
    SELECT r.exclusion_id, r.revision, GREATEST(r.starts_at, a.effective_at), LEAST(r.ends_at, a.archived_at, a.joined_other_service_at), r.recorded_at
    FROM assignment a CROSS JOIN public.almanac_maintenance_exclusion_revisions r
    WHERE (a.archived_at IS NULL OR a.archived_at > sqlc.arg(cutoff)::timestamptz)
      AND r.scope_kind = 'all_services' AND NOT r.tombstone
      AND tstzrange(r.starts_at, r.ends_at, '[)') @> sqlc.arg(cutoff)::timestamptz
)
SELECT c.exclusion_id, c.revision, c.starts_at, c.ends_at
FROM candidates c
WHERE c.recorded_at <= sqlc.arg(cutoff)::timestamptz
  AND c.revision = (
      SELECT latest.revision
      FROM public.almanac_maintenance_exclusion_revisions latest
      WHERE latest.exclusion_id = c.exclusion_id AND latest.recorded_at <= sqlc.arg(cutoff)::timestamptz
      ORDER BY latest.recorded_at DESC, latest.revision DESC LIMIT 1
  )
ORDER BY c.exclusion_id, c.revision
LIMIT 1;

-- name: CountMaintenancePreviewServices :one
SELECT count(*) FROM public.almanac_services
WHERE id = ANY(sqlc.arg(service_ids)::uuid[])
  AND created_at <= sqlc.arg(inventory_at)::timestamptz
  AND (archived_at IS NULL OR archived_at > sqlc.arg(inventory_at)::timestamptz);

-- name: ListMaintenancePreviewMembers :many
SELECT slo.id AS slo_id, slo.display_name AS slo_name, s.id AS service_id, s.display_name AS service_name
FROM public.almanac_slo_service_assignments a
JOIN public.almanac_slos slo ON slo.id = a.slo_id
JOIN public.almanac_services s ON s.id = a.service_id
WHERE a.slo_id > sqlc.arg(after_slo)::uuid
  AND a.effective_at <= sqlc.arg(inventory_at)::timestamptz
  AND (sqlc.arg(scope_kind)::text = 'slos' OR s.archived_at IS NULL OR s.archived_at > sqlc.arg(inventory_at)::timestamptz)
  AND CASE sqlc.arg(scope_kind)::text
    WHEN 'slos' THEN a.slo_id = ANY(sqlc.arg(slo_ids)::uuid[])
    WHEN 'services' THEN a.service_id = ANY(sqlc.arg(service_ids)::uuid[])
    ELSE true END
  AND NOT EXISTS (
      SELECT 1 FROM public.almanac_slo_service_assignments later
      WHERE later.slo_id = a.slo_id AND later.effective_at <= sqlc.arg(inventory_at)::timestamptz
        AND (later.effective_at, later.definition_revision) > (a.effective_at, a.definition_revision)
  )
ORDER BY a.slo_id LIMIT sqlc.arg(page_limit)::integer;

-- name: CountMaintenancePreviewMembers :one
SELECT count(*) FROM public.almanac_slo_service_assignments a
JOIN public.almanac_services s ON s.id = a.service_id
WHERE a.effective_at <= sqlc.arg(inventory_at)::timestamptz
  AND (sqlc.arg(scope_kind)::text = 'slos' OR s.archived_at IS NULL OR s.archived_at > sqlc.arg(inventory_at)::timestamptz)
  AND CASE sqlc.arg(scope_kind)::text
    WHEN 'slos' THEN a.slo_id = ANY(sqlc.arg(slo_ids)::uuid[])
    WHEN 'services' THEN a.service_id = ANY(sqlc.arg(service_ids)::uuid[])
    ELSE true END
  AND NOT EXISTS (
      SELECT 1 FROM public.almanac_slo_service_assignments later
      WHERE later.slo_id = a.slo_id AND later.effective_at <= sqlc.arg(inventory_at)::timestamptz
        AND (later.effective_at, later.definition_revision) > (a.effective_at, a.definition_revision)
  );

-- name: ReadMaintenanceProjectionReady :one
SELECT coalesce((
    SELECT p.complete
    FROM public.almanac_service_maintenance_projection p
    WHERE p.exclusion_id = sqlc.arg(exclusion_id) AND p.desired_revision = sqlc.arg(revision)::bigint
), false)::boolean;
