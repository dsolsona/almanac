-- Replace the old fixed-upper-bound checks without rewriting populated
-- relations. Keep this transaction separate from inventory reconciliation so
-- it releases ACCESS EXCLUSIVE before that migration begins.
ALTER TABLE public.almanac_maintenance_change_sets
    DROP CONSTRAINT almanac_maintenance_change_sets_range_count_check,
    ADD CONSTRAINT almanac_maintenance_change_sets_range_count_check CHECK (range_count >= 0) NOT VALID;
ALTER TABLE public.almanac_maintenance_changed_inputs
    DROP CONSTRAINT almanac_maintenance_changed_inputs_part_check,
    ADD CONSTRAINT almanac_maintenance_changed_inputs_part_check CHECK (part > 0) NOT VALID;
ALTER TABLE public.almanac_slo_exclusion_work
    DROP CONSTRAINT almanac_slo_exclusion_work_progress_part_check,
    ADD CONSTRAINT almanac_slo_exclusion_work_progress_part_check CHECK (progress_part >= 0) NOT VALID;

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
    ordinal integer := 0;
    range_count bigint;
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
    SELECT count(*) INTO range_count FROM unnest(changes);
    IF range_count > 32767 THEN
        -- This index decides whether reuse is safe. A conservative envelope
        -- denies reuse across the affected time while exact replay still reads
        -- the immutable multirange from almanac_slo_exclusion_changes.
        changes := tstzmultirange(range_merge(changes));
        range_count := 1;
    END IF;
    INSERT INTO public.almanac_maintenance_change_sets(slo_id, revision, scope_ordinal, range_count)
    VALUES (wanted_slo, wanted_revision, a.scope_ordinal, range_count);
    FOR piece IN SELECT unnest(changes) LOOP
        ordinal := ordinal + 1;
        INSERT INTO public.almanac_maintenance_changed_inputs(slo_id, revision, part, scope_ordinal, input_range)
        VALUES (wanted_slo, wanted_revision, ordinal, a.scope_ordinal, piece);
    END LOOP;
    UPDATE public.almanac_maintenance_compatibility SET indexed_through = wanted_revision WHERE slo_id = wanted_slo;
    RETURN true;
END $$;
