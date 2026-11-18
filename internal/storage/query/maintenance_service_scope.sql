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
SELECT cadence_seconds FROM public.almanac_maintenance_installation_grids
WHERE sqlc.arg(all_services)::boolean
UNION
SELECT g.cadence_seconds
FROM public.almanac_maintenance_service_grids g
JOIN public.almanac_services s ON s.id = g.service_id
WHERE NOT sqlc.arg(all_services)::boolean AND s.archived_at IS NULL
  AND g.service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY cadence_seconds;

-- A witness is sufficient to suppress an occurrence. Look up the membership
-- and immutable selector revision effective at the evaluation cutoff. Projection
-- state cannot participate in this decision. Timestamps establish eligibility
-- at the cutoff; the immutable revision sequence establishes precedence.
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
      ORDER BY latest.revision DESC LIMIT 1
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
SELECT EXISTS (
    SELECT 1 FROM public.almanac_service_maintenance_projection_completed c
    WHERE c.exclusion_id = sqlc.arg(exclusion_id) AND c.revision = sqlc.arg(revision)::bigint
)::boolean;

-- name: HasServiceMaintenanceProjection :one
SELECT EXISTS(SELECT 1 FROM public.almanac_service_maintenance_projection WHERE exclusion_id = $1)::boolean;

-- name: LockNextServiceMaintenanceProjection :one
SELECT p.exclusion_id, p.desired_revision, p.through_slo
FROM public.almanac_service_maintenance_projection p
JOIN public.almanac_maintenance_exclusions e ON e.id = p.exclusion_id AND e.current_revision = p.desired_revision
WHERE NOT p.complete
ORDER BY p.exclusion_id LIMIT 1 FOR UPDATE OF e SKIP LOCKED;

-- name: ListServiceMaintenanceProjectionSlos :many
SELECT s.id FROM public.almanac_slos s
WHERE s.id > sqlc.arg(after_slo)::uuid
  AND (
      EXISTS (
          SELECT 1 FROM public.almanac_service_maintenance_intervals(
              sqlc.arg(exclusion_id)::uuid, sqlc.arg(revision)::bigint, s.id, NULL
          )
      ) OR (
          SELECT c.included FROM public.almanac_slo_exclusion_changes c
          WHERE c.slo_id = s.id AND c.exclusion_id = sqlc.arg(exclusion_id)
          ORDER BY c.revision DESC LIMIT 1
      )
  )
ORDER BY s.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ApplyServiceMaintenanceProjectionMember :one
SELECT public.almanac_project_service_maintenance_member(
    sqlc.arg(slo_id)::uuid, sqlc.arg(exclusion_id)::uuid, sqlc.arg(revision)::bigint, sqlc.arg(now_at)::timestamptz
)::boolean;

-- name: AdvanceServiceMaintenanceProjection :execrows
UPDATE public.almanac_service_maintenance_projection SET through_slo = sqlc.arg(through_slo), complete = sqlc.arg(complete)
WHERE exclusion_id = sqlc.arg(exclusion_id) AND desired_revision = sqlc.arg(revision)::bigint;

-- name: LockServiceMaintenanceMembershipWork :one
SELECT w.slo_id, w.through_exclusion, w.affected_from FROM public.almanac_service_maintenance_membership_work w
JOIN public.almanac_slos s ON s.id = w.slo_id
ORDER BY w.slo_id LIMIT 1 FOR NO KEY UPDATE OF s SKIP LOCKED;

-- name: ListServiceMaintenanceMembershipTargets :many
SELECT p.exclusion_id, p.desired_revision
FROM public.almanac_service_maintenance_projection p
JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id = p.exclusion_id AND r.revision = p.desired_revision
WHERE p.exclusion_id > sqlc.arg(after_exclusion)::uuid AND r.ends_at > sqlc.arg(affected_from)::timestamptz
ORDER BY p.exclusion_id LIMIT sqlc.arg(page_limit)::integer;

-- name: AdvanceServiceMaintenanceMembershipWork :exec
UPDATE public.almanac_service_maintenance_membership_work SET through_exclusion = sqlc.arg(through_exclusion) WHERE slo_id = sqlc.arg(slo_id);

-- name: FinishServiceMaintenanceMembershipWork :exec
DELETE FROM public.almanac_service_maintenance_membership_work WHERE slo_id = $1;

-- name: CompleteServiceMaintenanceProjection :exec
INSERT INTO public.almanac_service_maintenance_projection_completed(exclusion_id, revision)
VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: FindMaintenanceSupersededAt :one
SELECT recorded_at
FROM public.almanac_maintenance_exclusion_revisions r
WHERE r.exclusion_id = sqlc.arg(exclusion_id) AND r.revision > sqlc.arg(revision)::bigint
ORDER BY r.revision LIMIT 1;

-- name: CountMaintenanceImpactMembers :one
SELECT count(DISTINCT p.slo_id)
FROM public.almanac_maintenance_impact_intervals(
    sqlc.arg(exclusion_id)::uuid, sqlc.arg(revision)::bigint, sqlc.arg(inventory_at)::timestamptz
) p;

-- name: ListMaintenanceImpactMembers :many
SELECT slo.id AS slo_id, slo.display_name AS slo_name, s.id AS service_id, s.display_name AS service_name,
       p.starts_at::timestamptz AS starts_at, p.ends_at::timestamptz AS ends_at
FROM public.almanac_maintenance_impact_intervals(
    sqlc.arg(exclusion_id)::uuid, sqlc.arg(revision)::bigint, sqlc.arg(inventory_at)::timestamptz
) p
JOIN public.almanac_slos slo ON slo.id = p.slo_id
JOIN public.almanac_services s ON s.id = p.service_id
WHERE (p.slo_id, p.starts_at) > (sqlc.arg(after_slo)::uuid, sqlc.arg(after_time)::timestamptz)
ORDER BY 1, 5
LIMIT sqlc.arg(page_limit)::integer;

-- name: HasPendingServiceMaintenance :one
SELECT public.almanac_slo_service_maintenance_pending(
    sqlc.arg(slo_id)::uuid, sqlc.arg(start_at)::timestamptz, sqlc.arg(end_at)::timestamptz
)::boolean;

-- name: ListScopedSloMaintenanceOverlaps :many
WITH latest AS MATERIALIZED (
    SELECT DISTINCT ON (c.exclusion_id) c.exclusion_id, c.exclusion_revision, c.included, c.effective_coverage
    FROM public.almanac_slo_exclusion_changes c
    WHERE c.slo_id = sqlc.arg(slo_id)
    ORDER BY c.exclusion_id, c.revision DESC
)
SELECT r.exclusion_id, r.revision, lower(segment)::timestamptz AS starts_at, upper(segment)::timestamptz AS ends_at
FROM latest c
JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id = c.exclusion_id AND r.revision = c.exclusion_revision
CROSS JOIN LATERAL unnest(public.almanac_effective_maintenance_coverage(sqlc.arg(slo_id), c.exclusion_id, c.exclusion_revision, c.effective_coverage)) segment
WHERE c.included AND NOT r.tombstone
  AND segment && tstzrange(sqlc.arg(window_start)::timestamptz, sqlc.arg(window_end)::timestamptz, '[)')
  AND (lower(segment), r.exclusion_id) > (sqlc.arg(after_time)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY starts_at, r.exclusion_id
LIMIT sqlc.arg(page_limit)::integer;

-- name: SummarizeScopedSloMaintenanceOverflow :one
WITH latest AS MATERIALIZED (
    SELECT DISTINCT ON (c.exclusion_id) c.exclusion_id, c.exclusion_revision, c.included, c.effective_coverage
    FROM public.almanac_slo_exclusion_changes c
    WHERE c.slo_id = sqlc.arg(slo_id)
    ORDER BY c.exclusion_id, c.revision DESC
)
SELECT count(*) AS window_count,
       min(greatest(lower(segment), sqlc.arg(window_start)::timestamptz))::timestamptz AS starts_at,
       max(least(upper(segment), sqlc.arg(window_end)::timestamptz))::timestamptz AS ends_at
FROM latest c
CROSS JOIN LATERAL unnest(public.almanac_effective_maintenance_coverage(sqlc.arg(slo_id), c.exclusion_id, c.exclusion_revision, c.effective_coverage)) segment
WHERE c.included
  AND segment && tstzrange(sqlc.arg(window_start)::timestamptz, sqlc.arg(window_end)::timestamptz, '[)')
  AND (lower(segment), c.exclusion_id) > (sqlc.arg(after_time)::timestamptz, sqlc.arg(after_id)::uuid);

-- A claim joins two rows and initially locks the parent only. Revalidate the
-- queue cursor in a fresh statement after all parent/authority locks are held.
-- name: LockObservedServiceMaintenanceProjection :one
SELECT p.exclusion_id
FROM public.almanac_service_maintenance_projection p
WHERE p.exclusion_id = sqlc.arg(exclusion_id)
  AND p.desired_revision = sqlc.arg(revision)::bigint
  AND p.through_slo = sqlc.arg(through_slo) AND NOT p.complete
FOR UPDATE SKIP LOCKED;

-- name: LockObservedServiceMaintenanceMembershipWork :one
SELECT w.slo_id
FROM public.almanac_service_maintenance_membership_work w
WHERE w.slo_id = sqlc.arg(slo_id) AND w.through_exclusion = sqlc.arg(through_exclusion)
  AND w.affected_from = sqlc.arg(affected_from)::timestamptz
FOR UPDATE SKIP LOCKED;
