-- Repair prospective assignment and projection behavior while retaining
-- the published schema checksums and immutable earlier calculation evidence.
LOCK TABLE public.almanac_services, public.almanac_slos IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE public.almanac_service_maintenance_authority
    ADD COLUMN inventory_revision bigint NOT NULL DEFAULT 0 CHECK (inventory_revision >= 0);

-- Existing jobs have no recorded lower bound. Keep their original work
-- conservatively eligible; new jobs retain the effective membership boundary.
ALTER TABLE public.almanac_service_maintenance_membership_work
    ADD COLUMN affected_from timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
    ADD CONSTRAINT almanac_service_maintenance_affected_from_finite CHECK (isfinite(affected_from)) NOT VALID;
ALTER TABLE public.almanac_service_maintenance_membership_work ALTER COLUMN affected_from DROP DEFAULT;

CREATE OR REPLACE FUNCTION public.almanac_record_active_slo_service() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    subject_id uuid;
    current_assignment record;
    prior_assignment record;
BEGIN
    subject_id := NEW.id;

    SELECT d.revision, d.service_id, d.cadence_seconds, GREATEST(NEW.updated_at, d.activated_at) AS effective_at
    INTO current_assignment
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id = s.id AND d.revision = s.definition_revision
    WHERE s.id = subject_id AND s.definition_revision = NEW.definition_revision;
    -- The deferred root may have passed through provisional revisions. Only
    -- the matching event supplies a selection time for the committed grid.
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT a.definition_revision, a.service_id, a.effective_at, d.cadence_seconds
    INTO prior_assignment
    FROM public.almanac_slo_service_assignments a
    JOIN public.almanac_slo_definitions d ON d.slo_id = a.slo_id AND d.revision = a.definition_revision
    WHERE a.slo_id = subject_id
    ORDER BY a.effective_at DESC, a.definition_revision DESC
    LIMIT 1;

    IF FOUND THEN
        IF prior_assignment.service_id = current_assignment.service_id
           AND prior_assignment.cadence_seconds = current_assignment.cadence_seconds THEN
            RETURN NULL;
        END IF;
        IF current_assignment.effective_at < prior_assignment.effective_at
           OR current_assignment.revision <= prior_assignment.definition_revision THEN
            RAISE EXCEPTION 'SLO service assignments must advance prospectively' USING ERRCODE = '23514';
        END IF;
    END IF;

    UPDATE public.almanac_service_maintenance_authority
    SET revision = revision + 1, inventory_revision = inventory_revision + 1 WHERE singleton;
    INSERT INTO public.almanac_slo_service_assignments(slo_id, definition_revision, service_id, effective_at)
    VALUES (subject_id, current_assignment.revision, current_assignment.service_id, current_assignment.effective_at);
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.almanac_fence_service_archive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.almanac_service_maintenance_authority
    SET revision = revision + 1, inventory_revision = inventory_revision + 1 WHERE singleton;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.almanac_enqueue_service_maintenance_join() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.almanac_service_maintenance_projection p
        JOIN public.almanac_maintenance_exclusion_revisions r
          ON r.exclusion_id = p.exclusion_id AND r.revision = p.desired_revision
        WHERE r.scope_kind <> 'slos' AND NOT r.tombstone AND r.ends_at > NEW.effective_at
    ) THEN
        INSERT INTO public.almanac_service_maintenance_membership_work(slo_id, affected_from)
        VALUES (NEW.slo_id, NEW.effective_at)
        ON CONFLICT (slo_id) DO UPDATE SET through_exclusion = DEFAULT,
            affected_from = least(almanac_service_maintenance_membership_work.affected_from, excluded.affected_from);
    END IF;
    RETURN NULL;
END $$;

-- The earlier journal skipped same-service cadence-only changes even though
-- the grid counters tracked the active root. Bring the journal up to the
-- current definition before its insert trigger takes over those counters.
WITH seeded AS (
    INSERT INTO public.almanac_slo_service_assignments(slo_id, definition_revision, service_id, effective_at)
    SELECT s.id, d.revision, d.service_id, greatest(d.activated_at, a.effective_at)
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id = s.id AND d.revision = s.definition_revision
    JOIN LATERAL (
        SELECT entry.definition_revision, entry.service_id, entry.effective_at, old.cadence_seconds
        FROM public.almanac_slo_service_assignments entry
        JOIN public.almanac_slo_definitions old ON old.slo_id = entry.slo_id AND old.revision = entry.definition_revision
        WHERE entry.slo_id = s.id
        ORDER BY entry.effective_at DESC, entry.definition_revision DESC LIMIT 1
    ) a ON true
    WHERE d.revision > a.definition_revision AND (d.service_id, d.cadence_seconds) <> (a.service_id, a.cadence_seconds)
    RETURNING slo_id
)
UPDATE public.almanac_service_maintenance_authority
SET revision = revision + 1, inventory_revision = inventory_revision + 1
WHERE singleton AND EXISTS (SELECT 1 FROM seeded);

DROP TRIGGER almanac_slo_grid_initial ON public.almanac_slos;
DROP TRIGGER almanac_slo_grid_changed ON public.almanac_slos;

CREATE OR REPLACE FUNCTION public.almanac_record_maintenance_grid() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    previous_grid record;
    next_cadence integer;
BEGIN
    SELECT cadence_seconds INTO STRICT next_cadence
    FROM public.almanac_slo_definitions
    WHERE slo_id = NEW.slo_id AND revision = NEW.definition_revision;

    SELECT a.service_id, d.cadence_seconds INTO previous_grid
    FROM public.almanac_slo_service_assignments a
    JOIN public.almanac_slo_definitions d ON d.slo_id = a.slo_id AND d.revision = a.definition_revision
    WHERE a.slo_id = NEW.slo_id
      AND (a.effective_at, a.definition_revision) < (NEW.effective_at, NEW.definition_revision)
    ORDER BY a.effective_at DESC, a.definition_revision DESC LIMIT 1;

    IF FOUND THEN
        PERFORM public.almanac_adjust_maintenance_service_grid(previous_grid.service_id, previous_grid.cadence_seconds, -1);
    END IF;
    PERFORM public.almanac_adjust_maintenance_service_grid(NEW.service_id, next_cadence, 1);
    RETURN NULL;
END $$;

CREATE TRIGGER almanac_slo_grid_assignment
    AFTER INSERT ON public.almanac_slo_service_assignments
    FOR EACH ROW EXECUTE FUNCTION public.almanac_record_maintenance_grid();

-- The coverage function's return shape adds the effective cadence. Replace
-- the function identity before installing the consumers of the new shape.
DROP FUNCTION public.almanac_service_maintenance_intervals(uuid, bigint, uuid, timestamptz);

CREATE FUNCTION public.almanac_service_maintenance_intervals(
    wanted_exclusion uuid, wanted_revision bigint, wanted_slo uuid, inventory_at timestamptz
) RETURNS TABLE(slo_id uuid, service_id uuid, starts_at timestamptz, ends_at timestamptz, cadence_seconds integer)
LANGUAGE sql STABLE AS $$
    WITH bounds AS MATERIALIZED (
        SELECT r.starts_at, r.ends_at, r.scope_kind
        FROM public.almanac_maintenance_exclusion_revisions r
        WHERE r.exclusion_id = wanted_exclusion AND r.revision = wanted_revision AND NOT r.tombstone
          AND r.scope_kind <> 'slos'
    ), assignments AS (
        SELECT a.slo_id, a.service_id, a.effective_at, d.cadence_seconds,
               lead(a.effective_at) OVER (PARTITION BY a.slo_id ORDER BY a.effective_at, a.definition_revision) AS leaves_at
        FROM public.almanac_slo_service_assignments a
        JOIN public.almanac_slo_definitions d ON d.slo_id = a.slo_id AND d.revision = a.definition_revision
        CROSS JOIN bounds r
        WHERE (wanted_slo IS NULL OR a.slo_id = wanted_slo)
          AND a.effective_at < r.ends_at AND (inventory_at IS NULL OR a.effective_at <= inventory_at)
    ), participation AS (
        SELECT a.slo_id, a.service_id, a.cadence_seconds, greatest(a.effective_at, r.starts_at) AS starts_at,
               least(a.leaves_at, s.archived_at, r.ends_at) AS ends_at
        FROM assignments a
        JOIN public.almanac_services s ON s.id = a.service_id
        CROSS JOIN bounds r
        WHERE r.scope_kind = 'all_services' OR EXISTS (
            SELECT 1 FROM public.almanac_maintenance_service_selectors selected
            WHERE selected.exclusion_id = wanted_exclusion AND selected.revision = wanted_revision
              AND selected.service_id = a.service_id
        )
    )
    SELECT p.slo_id, p.service_id, p.starts_at, p.ends_at, p.cadence_seconds FROM participation p
    WHERE p.starts_at < p.ends_at;
$$;

CREATE OR REPLACE FUNCTION public.almanac_project_service_maintenance_member(
    wanted_slo uuid, wanted_exclusion uuid, wanted_revision bigint, now_at timestamptz
) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    previous public.almanac_slo_exclusion_changes;
    epoch constant timestamptz := '1970-01-01 UTC';
    old_coverage tstzmultirange := '{}';
    new_coverage tstzmultirange := '{}';
    affected tstzrange;
    next_revision bigint;
    changed boolean;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.almanac_service_maintenance_projection p
        JOIN public.almanac_maintenance_exclusions e ON e.id = p.exclusion_id AND e.current_revision = p.desired_revision
        WHERE p.exclusion_id = wanted_exclusion AND p.desired_revision = wanted_revision
    ) THEN RETURN false; END IF;
    -- A mode change to explicit was already installed by the mutation for
    -- selected SLOs. The remaining old service members still need removal.
    IF EXISTS (
        SELECT 1 FROM public.almanac_maintenance_exclusion_scopes s
        JOIN public.almanac_maintenance_exclusion_revisions r
          ON r.exclusion_id = s.exclusion_id AND r.revision = s.revision
        WHERE s.exclusion_id = wanted_exclusion AND s.revision = wanted_revision
          AND s.slo_id = wanted_slo AND r.scope_kind = 'slos' AND NOT r.tombstone
    ) THEN RETURN false; END IF;

    SELECT * INTO previous FROM public.almanac_slo_exclusion_changes c
    WHERE c.slo_id = wanted_slo AND c.exclusion_id = wanted_exclusion
    ORDER BY c.revision DESC LIMIT 1;
    IF FOUND AND previous.included THEN
        old_coverage := public.almanac_effective_maintenance_coverage(
            wanted_slo, wanted_exclusion, previous.exclusion_revision, previous.effective_coverage);
    END IF;
    -- Union adjacent participation on the same grid before rounding. A
    -- service reassignment within the selector does not split an observation.
    WITH unioned AS (
        SELECT make_interval(secs => p.cadence_seconds) AS cadence,
               unnest(range_agg(tstzrange(p.starts_at, p.ends_at, '[)'))) AS part
        FROM public.almanac_service_maintenance_intervals(wanted_exclusion, wanted_revision, wanted_slo, NULL) p
        GROUP BY p.cadence_seconds
    ), aligned AS (
        SELECT CASE WHEN a.floor_start = lower(a.part) THEN a.floor_start ELSE a.floor_start + a.cadence END AS starts_at,
               date_bin(a.cadence, upper(a.part), epoch) AS ends_at
        FROM (SELECT cadence, part, date_bin(cadence, lower(part), epoch) AS floor_start FROM unioned) a
    )
    SELECT coalesce(range_agg(tstzrange(a.starts_at, a.ends_at, '[)')), '{}') INTO new_coverage
    FROM aligned a WHERE a.starts_at < a.ends_at;

    IF old_coverage IS NULL THEN
        RAISE EXCEPTION 'maintenance projection requires the previous population' USING ERRCODE = '23514';
    END IF;
    changed := old_coverage <> new_coverage;
    IF NOT changed AND (isempty(new_coverage) OR previous.exclusion_revision = wanted_revision) THEN
        RETURN false;
    END IF;
    UPDATE public.almanac_slo_exclusion_state SET revision = revision + 1, changed_at = now_at
    WHERE slo_id = wanted_slo RETURNING revision INTO STRICT next_revision;
    INSERT INTO public.almanac_slo_exclusion_changes(
        slo_id, revision, exclusion_id, exclusion_revision, included, coverage_changed, recorded_at, effective_coverage
    ) VALUES (
        wanted_slo, next_revision, wanted_exclusion, wanted_revision, NOT isempty(new_coverage), changed, now_at, new_coverage
    );
    IF changed THEN affected := range_merge(old_coverage + new_coverage); END IF;
    INSERT INTO public.almanac_slo_exclusion_work(
        slo_id, desired_revision, work_version, affected_start, affected_end, next_attempt_at
    ) VALUES (wanted_slo, next_revision, 1, lower(affected), upper(affected), now_at)
    ON CONFLICT (slo_id) DO UPDATE SET desired_revision = excluded.desired_revision,
        work_version = almanac_slo_exclusion_work.work_version + 1,
        affected_start = least(almanac_slo_exclusion_work.affected_start, excluded.affected_start),
        affected_end = greatest(almanac_slo_exclusion_work.affected_end, excluded.affected_end),
        next_attempt_at = least(almanac_slo_exclusion_work.next_attempt_at, excluded.next_attempt_at);
    RETURN true;
END $$;

CREATE OR REPLACE FUNCTION public.almanac_maintenance_impact_intervals(
    wanted_exclusion uuid, wanted_revision bigint, inventory_at timestamptz
) RETURNS TABLE(slo_id uuid, service_id uuid, starts_at timestamptz, ends_at timestamptz)
LANGUAGE sql STABLE AS $$
    SELECT p.slo_id, p.service_id, lower(p.part), upper(p.part)
    FROM (
        SELECT i.slo_id, i.service_id, unnest(range_agg(tstzrange(i.starts_at, i.ends_at, '[)'))) AS part
        FROM public.almanac_service_maintenance_intervals(wanted_exclusion, wanted_revision, NULL, inventory_at) i
        GROUP BY i.slo_id, i.service_id
    ) p
    UNION ALL
    SELECT selected.slo_id, a.service_id, r.starts_at, r.ends_at
    FROM public.almanac_maintenance_exclusion_revisions r
    JOIN public.almanac_maintenance_exclusion_scopes selected
      ON selected.exclusion_id = r.exclusion_id AND selected.revision = r.revision
    CROSS JOIN LATERAL (
        SELECT assignment.service_id
        FROM public.almanac_slo_service_assignments assignment
        WHERE assignment.slo_id = selected.slo_id AND assignment.effective_at <= inventory_at
        ORDER BY assignment.effective_at DESC, assignment.definition_revision DESC LIMIT 1
    ) a
    WHERE r.exclusion_id = wanted_exclusion AND r.revision = wanted_revision AND r.scope_kind = 'slos' AND NOT r.tombstone;
$$;

CREATE OR REPLACE FUNCTION public.almanac_slo_service_maintenance_pending(
    wanted_slo uuid, requested_start timestamptz, requested_end timestamptz
) RETURNS boolean LANGUAGE sql STABLE AS $$
    WITH pending AS MATERIALIZED (
        SELECT p.exclusion_id, p.desired_revision
        FROM public.almanac_service_maintenance_projection p
        WHERE NOT p.complete AND p.through_slo < wanted_slo
        UNION
        SELECT p.exclusion_id, p.desired_revision
        FROM public.almanac_service_maintenance_membership_work w
        JOIN public.almanac_service_maintenance_projection p ON p.exclusion_id > w.through_exclusion
        JOIN public.almanac_maintenance_exclusion_revisions r ON r.exclusion_id = p.exclusion_id AND r.revision = p.desired_revision
        WHERE w.slo_id = wanted_slo AND r.ends_at > w.affected_from
    ), requested AS MATERIALIZED (
        SELECT tstzrange(requested_start, requested_end, '[)') AS bounds
        WHERE requested_start < requested_end
    )
    SELECT EXISTS (
        SELECT 1 FROM pending p CROSS JOIN requested
        WHERE EXISTS (
            SELECT 1 FROM public.almanac_service_maintenance_intervals(p.exclusion_id, p.desired_revision, wanted_slo, NULL) covers
            WHERE tstzrange(covers.starts_at, covers.ends_at, '[)') && requested.bounds
        ) OR EXISTS (
            SELECT 1
            FROM (
                SELECT c.included, c.exclusion_revision, c.effective_coverage
                FROM public.almanac_slo_exclusion_changes c
                WHERE c.slo_id = wanted_slo AND c.exclusion_id = p.exclusion_id
                ORDER BY c.revision DESC LIMIT 1
            ) latest
            WHERE latest.included AND public.almanac_effective_maintenance_coverage(
                wanted_slo, p.exclusion_id, latest.exclusion_revision, latest.effective_coverage
            ) && requested.bounds
        )
    );
$$;

CREATE FUNCTION public.almanac_service_maintenance_work_pending()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM public.almanac_service_maintenance_projection WHERE NOT complete)
        OR EXISTS (SELECT 1 FROM public.almanac_service_maintenance_membership_work);
$$;

-- The previous projector could round historical coverage to today's grid.
-- Reproject the known history of SLOs that have recorded multiple grids.
INSERT INTO public.almanac_service_maintenance_membership_work(slo_id, affected_from)
SELECT a.slo_id, min(a.effective_at)
FROM public.almanac_slo_service_assignments a
JOIN public.almanac_slo_definitions d ON d.slo_id = a.slo_id AND d.revision = a.definition_revision
WHERE EXISTS (SELECT 1 FROM public.almanac_service_maintenance_projection)
GROUP BY a.slo_id
HAVING min(d.cadence_seconds) <> max(d.cadence_seconds)
ON CONFLICT (slo_id) DO UPDATE SET through_exclusion = DEFAULT,
    affected_from = least(almanac_service_maintenance_membership_work.affected_from, excluded.affected_from);
