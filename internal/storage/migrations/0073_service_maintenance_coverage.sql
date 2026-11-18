-- A service selector can produce several disjoint intervals for the same
-- window/SLO. Preserve their full set on each immutable exclusion event.
ALTER TABLE public.almanac_slo_exclusion_changes
    ADD COLUMN effective_coverage tstzmultirange,
    ADD CONSTRAINT almanac_service_effective_coverage_shape CHECK (
        effective_coverage IS NULL OR included = (NOT isempty(effective_coverage)));

DO $$
DECLARE scope_fk name;
BEGIN
    SELECT conname INTO STRICT scope_fk
    FROM pg_constraint
    WHERE conrelid = 'public.almanac_maintenance_members'::regclass
      AND confrelid = 'public.almanac_maintenance_exclusion_scopes'::regclass
      AND contype = 'f';
    EXECUTE format('ALTER TABLE public.almanac_maintenance_members DROP CONSTRAINT %I', scope_fk);
END $$;
ALTER TABLE public.almanac_maintenance_members
    DROP CONSTRAINT almanac_maintenance_members_pkey,
    ADD PRIMARY KEY (build_id, exclusion_id, starts_at),
    ADD FOREIGN KEY (exclusion_id, exclusion_revision)
        REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id, revision),
    ADD FOREIGN KEY (slo_id) REFERENCES public.almanac_slos(id);

CREATE FUNCTION public.almanac_effective_maintenance_coverage(
    wanted_slo uuid, wanted_exclusion uuid, wanted_revision bigint, stored_coverage tstzmultirange
) RETURNS tstzmultirange LANGUAGE sql STABLE AS $$
    SELECT COALESCE(stored_coverage, (
        SELECT tstzmultirange(tstzrange(r.starts_at, r.ends_at, '[)'))
        FROM public.almanac_maintenance_exclusion_revisions r
        JOIN public.almanac_maintenance_exclusion_scopes s
          ON s.exclusion_id = r.exclusion_id AND s.revision = r.revision
        WHERE r.exclusion_id = wanted_exclusion AND r.revision = wanted_revision
          AND s.slo_id = wanted_slo AND r.scope_kind = 'slos' AND NOT r.tombstone
    ));
$$;

-- Reuse the exact change-set index for service and explicit coverage. A
-- metadata-only revision or overlap may have an empty symmetric difference.
CREATE OR REPLACE FUNCTION public.almanac_analyze_maintenance_change(
    wanted_slo uuid, wanted_revision bigint
) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    a public.almanac_maintenance_compatibility;
    e public.almanac_slo_exclusion_changes;
    previous public.almanac_slo_exclusion_changes;
    old_coverage tstzmultirange := '{}';
    new_coverage tstzmultirange := '{}';
    changes tstzmultirange;
    piece tstzrange;
    ordinal smallint := 0;
BEGIN
    INSERT INTO public.almanac_maintenance_compatibility(slo_id)
    VALUES (wanted_slo) ON CONFLICT DO NOTHING;
    SELECT * INTO a FROM public.almanac_maintenance_compatibility WHERE slo_id = wanted_slo FOR UPDATE;
    IF wanted_revision <= a.indexed_through THEN RETURN true; END IF;
    IF a.unavailable OR a.indexed_through = 9223372036854775807
       OR wanted_revision <> a.indexed_through + 1 THEN RETURN false; END IF;
    SELECT * INTO e FROM public.almanac_slo_exclusion_changes
    WHERE slo_id = wanted_slo AND revision = wanted_revision;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT * INTO previous FROM public.almanac_slo_exclusion_changes
    WHERE slo_id = wanted_slo AND exclusion_id = e.exclusion_id AND revision < wanted_revision
    ORDER BY revision DESC LIMIT 1;
    IF FOUND AND previous.included THEN
        old_coverage := public.almanac_effective_maintenance_coverage(
            wanted_slo, previous.exclusion_id, previous.exclusion_revision, previous.effective_coverage);
    END IF;
    IF e.included THEN
        new_coverage := public.almanac_effective_maintenance_coverage(
            wanted_slo, e.exclusion_id, e.exclusion_revision, e.effective_coverage);
    END IF;
    IF old_coverage IS NULL OR new_coverage IS NULL THEN
        UPDATE public.almanac_maintenance_compatibility SET unavailable = true WHERE slo_id = wanted_slo;
        RETURN false;
    END IF;
    changes := (old_coverage - new_coverage) + (new_coverage - old_coverage);
    INSERT INTO public.almanac_maintenance_change_sets(slo_id, revision, scope_ordinal, range_count)
    VALUES (wanted_slo, wanted_revision, a.scope_ordinal, (SELECT count(*) FROM unnest(changes)));
    FOR piece IN SELECT unnest(changes) LOOP
        ordinal := ordinal + 1;
        INSERT INTO public.almanac_maintenance_changed_inputs(slo_id, revision, part, scope_ordinal, input_range)
        VALUES (wanted_slo, wanted_revision, ordinal, a.scope_ordinal, piece);
    END LOOP;
    UPDATE public.almanac_maintenance_compatibility SET indexed_through = wanted_revision WHERE slo_id = wanted_slo;
    RETURN true;
END $$;
