-- Unwired compiler. Per-turn admission and scheduling have no production default.
CREATE TABLE public.almanac_maintenance_input_guards (
 slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id), touched boolean NOT NULL DEFAULT false
);
CREATE TABLE public.almanac_maintenance_builds (
 id uuid PRIMARY KEY, slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
 target_revision bigint NOT NULL CHECK(target_revision>=0),
 phase text NOT NULL DEFAULT 'replay' CHECK(phase IN ('replay','union','complete','unavailable','cancelled')),
 raw_cursor bigint NOT NULL DEFAULT 0 CHECK(raw_cursor>=0 AND raw_cursor<=target_revision),
 member_start timestamptz, member_id uuid,
 frontier_start timestamptz, frontier_end timestamptz,
 cumulative_us bigint NOT NULL DEFAULT 0 CHECK(cumulative_us>=0),
 lease_token uuid, lease_until timestamptz,
 created_at timestamptz NOT NULL, completed_at timestamptz,
 UNIQUE(slo_id,target_revision),
 CHECK ((member_start IS NULL)=(member_id IS NULL)),
 CHECK ((frontier_start IS NULL)=(frontier_end IS NULL)),
 CHECK (frontier_start<frontier_end),
 CHECK ((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK ((phase='complete')=(completed_at IS NOT NULL))
);
CREATE INDEX almanac_maintenance_build_pin_idx ON public.almanac_maintenance_builds(slo_id,target_revision) WHERE phase<>'cancelled';
CREATE TABLE public.almanac_maintenance_members (
 build_id uuid NOT NULL REFERENCES public.almanac_maintenance_builds(id),
 exclusion_id uuid NOT NULL, exclusion_revision bigint NOT NULL, slo_id uuid NOT NULL,
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 PRIMARY KEY(build_id,exclusion_id),
 FOREIGN KEY(exclusion_id,exclusion_revision,slo_id) REFERENCES public.almanac_maintenance_exclusion_scopes(exclusion_id,revision,slo_id),
 CHECK (isfinite(starts_at) AND isfinite(ends_at) AND starts_at<ends_at)
);
CREATE INDEX almanac_maintenance_member_order_idx ON public.almanac_maintenance_members(build_id,starts_at,exclusion_id);
CREATE TABLE public.almanac_maintenance_coverage (
 build_id uuid NOT NULL REFERENCES public.almanac_maintenance_builds(id),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 cumulative_us bigint NOT NULL CHECK(cumulative_us>0),
 PRIMARY KEY(build_id,starts_at),
 CHECK (isfinite(starts_at) AND isfinite(ends_at) AND starts_at<ends_at)
);
CREATE INDEX almanac_maintenance_change_provenance_idx ON public.almanac_slo_exclusion_changes(slo_id,exclusion_id,exclusion_revision,revision) WHERE included;

-- Actual writes, rather than unchanged row locks, make stale RR cleanup fail
-- with serialization instead of overlooking a pin committed after its snapshot.
CREATE FUNCTION public.almanac_guard_maintenance_input_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO public.almanac_maintenance_input_guards(slo_id) VALUES(OLD.slo_id)
 ON CONFLICT(slo_id) DO UPDATE SET touched=NOT almanac_maintenance_input_guards.touched;
 IF TG_TABLE_NAME='almanac_slo_exclusion_changes' THEN
  IF EXISTS(SELECT 1 FROM public.almanac_maintenance_builds b WHERE b.slo_id=OLD.slo_id AND b.target_revision>=OLD.revision AND b.phase<>'cancelled') THEN
   RAISE EXCEPTION 'maintenance compiler input is pinned' USING ERRCODE='23503';
  END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM public.almanac_slo_exclusion_changes c JOIN public.almanac_maintenance_builds b ON b.slo_id=c.slo_id AND b.target_revision>=c.revision AND b.phase<>'cancelled'
   WHERE c.slo_id=OLD.slo_id AND c.exclusion_id=OLD.exclusion_id AND c.exclusion_revision=OLD.revision AND c.included) THEN
   RAISE EXCEPTION 'maintenance compiler scope is pinned' USING ERRCODE='23503';
  END IF;
 END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER almanac_maintenance_change_pin BEFORE DELETE ON public.almanac_slo_exclusion_changes FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_input_delete();
CREATE TRIGGER almanac_maintenance_scope_pin BEFORE DELETE ON public.almanac_maintenance_exclusion_scopes FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_input_delete();

CREATE FUNCTION public.almanac_guard_maintenance_compiler_stage() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE stage text;
BEGIN
 SELECT phase INTO stage FROM public.almanac_maintenance_builds WHERE id=NEW.build_id FOR UPDATE;
 IF (TG_TABLE_NAME='almanac_maintenance_members' AND stage<>'replay') OR
    (TG_TABLE_NAME='almanac_maintenance_coverage' AND (stage<>'union' OR TG_OP='UPDATE')) THEN
  RAISE EXCEPTION 'maintenance compiler stage is sealed' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_member_seal BEFORE INSERT OR UPDATE ON public.almanac_maintenance_members FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_compiler_stage();
CREATE TRIGGER almanac_maintenance_coverage_seal BEFORE INSERT OR UPDATE ON public.almanac_maintenance_coverage FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_compiler_stage();
-- DELETE remains available to future bounded retention after evidence references
-- are released. This foundation has no cleanup caller or deletion of complete data.

CREATE FUNCTION public.almanac_guard_maintenance_build_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.slo_id,NEW.target_revision,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.slo_id,OLD.target_revision,OLD.created_at)
    OR NEW.raw_cursor<OLD.raw_cursor
    OR (OLD.phase='union' AND NEW.phase='replay')
    OR (OLD.phase IN ('complete','unavailable','cancelled') AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'maintenance compiler identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_build_identity BEFORE UPDATE ON public.almanac_maintenance_builds FOR EACH ROW EXECUTE FUNCTION public.almanac_guard_maintenance_build_identity();
