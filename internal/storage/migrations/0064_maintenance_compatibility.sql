-- Unwired compatibility preparation. No all-history migration backfill.
CREATE TABLE public.almanac_maintenance_compatibility (
 slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id),
 scope_ordinal bigint GENERATED ALWAYS AS IDENTITY UNIQUE CHECK(scope_ordinal>0),
 indexed_through bigint NOT NULL DEFAULT 0 CHECK(indexed_through>=0),
 unavailable boolean NOT NULL DEFAULT false,
 UNIQUE(slo_id,scope_ordinal)
);
CREATE TABLE public.almanac_maintenance_change_sets (
 slo_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>0), scope_ordinal bigint NOT NULL,
 range_count smallint NOT NULL CHECK(range_count BETWEEN 0 AND 2),
 PRIMARY KEY(slo_id,revision), UNIQUE(slo_id,revision,scope_ordinal),
 FOREIGN KEY(slo_id,scope_ordinal) REFERENCES public.almanac_maintenance_compatibility(slo_id,scope_ordinal),
 FOREIGN KEY(slo_id,revision) REFERENCES public.almanac_slo_exclusion_changes(slo_id,revision)
);
CREATE TABLE public.almanac_maintenance_changed_inputs (
 slo_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>0), part smallint NOT NULL CHECK(part BETWEEN 1 AND 2),
 scope_ordinal bigint NOT NULL CHECK(scope_ordinal>0), input_range tstzrange NOT NULL,
 scope_key int8range GENERATED ALWAYS AS (int8range(scope_ordinal,CASE WHEN scope_ordinal=9223372036854775807 THEN NULL ELSE scope_ordinal+1 END,'[)')) STORED,
 revision_key int8range GENERATED ALWAYS AS (int8range(revision,CASE WHEN revision=9223372036854775807 THEN NULL ELSE revision+1 END,'[)')) STORED,
 PRIMARY KEY(slo_id,revision,part),
 FOREIGN KEY(slo_id,revision,scope_ordinal) REFERENCES public.almanac_maintenance_change_sets(slo_id,revision,scope_ordinal),
 CHECK(NOT isempty(input_range) AND NOT lower_inf(input_range) AND NOT upper_inf(input_range) AND isfinite(lower(input_range)) AND isfinite(upper(input_range)))
);
-- Built-in 3D GiST was selected by the retained 200k-row scoped prototype.
CREATE INDEX almanac_maintenance_compatibility_gist ON public.almanac_maintenance_changed_inputs USING gist(scope_key,input_range,revision_key);
CREATE TRIGGER almanac_maintenance_changeset_immutable BEFORE UPDATE OR DELETE ON public.almanac_maintenance_change_sets FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();
CREATE TRIGGER almanac_maintenance_input_range_immutable BEFORE UPDATE OR DELETE ON public.almanac_maintenance_changed_inputs FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();

CREATE FUNCTION public.almanac_analyze_maintenance_change(wanted_slo uuid,wanted_revision bigint) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE a public.almanac_maintenance_compatibility; e public.almanac_slo_exclusion_changes; previous public.almanac_slo_exclusion_changes;
 old_range tstzrange := 'empty'; new_range tstzrange := 'empty'; changes tstzmultirange; piece tstzrange; ordinal smallint := 0;
BEGIN
 INSERT INTO public.almanac_maintenance_compatibility(slo_id) VALUES(wanted_slo) ON CONFLICT DO NOTHING;
 SELECT * INTO a FROM public.almanac_maintenance_compatibility WHERE slo_id=wanted_slo FOR UPDATE;
 IF wanted_revision<=a.indexed_through THEN RETURN true; END IF;
 IF a.unavailable OR a.indexed_through=9223372036854775807 OR wanted_revision<>a.indexed_through+1 THEN RETURN false; END IF;
 SELECT * INTO e FROM public.almanac_slo_exclusion_changes WHERE slo_id=wanted_slo AND revision=wanted_revision;
 IF NOT FOUND THEN RETURN false; END IF;
 SELECT * INTO previous FROM public.almanac_slo_exclusion_changes
 WHERE slo_id=wanted_slo AND exclusion_id=e.exclusion_id AND revision<wanted_revision ORDER BY revision DESC LIMIT 1;
 IF FOUND AND previous.included THEN
  SELECT tstzrange(starts_at,ends_at,'[)') INTO old_range FROM public.almanac_maintenance_exclusion_revisions WHERE exclusion_id=previous.exclusion_id AND revision=previous.exclusion_revision;
 END IF;
 IF e.included THEN
  SELECT tstzrange(r.starts_at,r.ends_at,'[)') INTO new_range FROM public.almanac_maintenance_exclusion_revisions r
  WHERE r.exclusion_id=e.exclusion_id AND r.revision=e.exclusion_revision AND NOT r.tombstone
  AND EXISTS(SELECT 1 FROM public.almanac_maintenance_exclusion_scopes s WHERE s.exclusion_id=e.exclusion_id AND s.revision=e.exclusion_revision AND s.slo_id=e.slo_id);
 END IF;
 IF old_range IS NULL OR new_range IS NULL THEN
  UPDATE public.almanac_maintenance_compatibility SET unavailable=true WHERE slo_id=wanted_slo; RETURN false;
 END IF;
 changes := (tstzmultirange(old_range)-tstzmultirange(new_range)) + (tstzmultirange(new_range)-tstzmultirange(old_range));
 INSERT INTO public.almanac_maintenance_change_sets(slo_id,revision,scope_ordinal,range_count)
 VALUES(wanted_slo,wanted_revision,a.scope_ordinal,(SELECT count(*) FROM unnest(changes)));
 FOR piece IN SELECT unnest(changes) LOOP
  ordinal := ordinal+1;
  INSERT INTO public.almanac_maintenance_changed_inputs(slo_id,revision,part,scope_ordinal,input_range) VALUES(wanted_slo,wanted_revision,ordinal,a.scope_ordinal,piece);
 END LOOP;
 UPDATE public.almanac_maintenance_compatibility SET indexed_through=wanted_revision WHERE slo_id=wanted_slo;
 RETURN true;
END $$;
CREATE FUNCTION public.almanac_analyze_inserted_maintenance_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM public.almanac_analyze_maintenance_change(NEW.slo_id,NEW.revision); RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_change_analyze AFTER INSERT ON public.almanac_slo_exclusion_changes FOR EACH ROW EXECUTE FUNCTION public.almanac_analyze_inserted_maintenance_change();

CREATE TABLE public.almanac_maintenance_comparisons (
 id uuid PRIMARY KEY, first_build uuid NOT NULL REFERENCES public.almanac_maintenance_builds(id), second_build uuid NOT NULL REFERENCES public.almanac_maintenance_builds(id),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','equal','different','cancelled')),
 after_start timestamptz, compared bigint NOT NULL DEFAULT 0 CHECK(compared>=0),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, UNIQUE(first_build,second_build)
);
CREATE FUNCTION public.almanac_guard_maintenance_comparison() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.first_build,NEW.second_build,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.first_build,OLD.second_build,OLD.created_at)
 OR NEW.compared<OLD.compared OR (OLD.after_start IS NOT NULL AND (NEW.after_start IS NULL OR NEW.after_start<OLD.after_start))
 OR (OLD.state<>'pending' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'maintenance comparison identity or terminal result is immutable' USING ERRCODE='23514';
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_comparison_identity BEFORE UPDATE ON public.almanac_maintenance_comparisons FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_comparison();
CREATE FUNCTION public.almanac_guard_complete_maintenance_coverage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE stage text;
BEGIN
 SELECT phase INTO stage FROM public.almanac_maintenance_builds WHERE id=OLD.build_id FOR UPDATE;
 IF stage='complete' THEN RAISE EXCEPTION 'complete maintenance coverage is retained' USING ERRCODE='23503'; END IF;
 RETURN OLD;
END $$;
-- No cleanup caller exists. Future bounded retention needs an explicit proof
-- invalidation/preparation-floor protocol before releasing these protected inputs.
CREATE TRIGGER almanac_maintenance_complete_coverage_pin BEFORE DELETE ON public.almanac_maintenance_coverage FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_complete_maintenance_coverage();
CREATE FUNCTION public.almanac_guard_maintenance_compatibility() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'maintenance compatibility authority is retained' USING ERRCODE='23514'; END IF;
 IF (NEW.slo_id,NEW.scope_ordinal) IS DISTINCT FROM (OLD.slo_id,OLD.scope_ordinal) OR NEW.indexed_through<OLD.indexed_through THEN
  RAISE EXCEPTION 'maintenance compatibility identity is immutable' USING ERRCODE='23514';
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_compatibility_identity BEFORE UPDATE OR DELETE ON public.almanac_maintenance_compatibility FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_compatibility();
