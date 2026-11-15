-- Unwired shared preparation. Natural keys identify immutable artifacts; source
-- hours continue to use the canonical dirty queue and are not represented here.
CREATE TABLE public.almanac_maintenance_preparation (
 id uuid PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('mask','compatibility','scoped_hour','comparison')),
 slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
 target_revision bigint NOT NULL CHECK(target_revision>=0),
 build_id uuid NOT NULL REFERENCES public.almanac_maintenance_builds(id),
 second_build uuid REFERENCES public.almanac_maintenance_builds(id),
 sli_id uuid REFERENCES public.almanac_slis(id),
 definition_revision bigint CHECK(definition_revision>0),
 measurement_revision bigint CHECK(measurement_revision>0),
 hour_start timestamptz,
 source_generation bigint CHECK(source_generation>=0),
 priority text NOT NULL CHECK(priority IN ('live','historical')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','complete','unavailable','obsolete','exhausted')),
 available_at timestamptz NOT NULL,
 last_tick timestamptz,
 lease_token uuid,
 lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0),
 lease_until timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 reason text NOT NULL DEFAULT '' CHECK(reason IN ('','dependency_pending','database_backpressure','preparation_failed','history_unavailable','invalid_facts','source_obsolete')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CHECK((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK(state='pending' OR lease_token IS NULL),
 CHECK((kind='scoped_hour' AND sli_id IS NOT NULL AND definition_revision IS NOT NULL AND measurement_revision IS NOT NULL AND hour_start IS NOT NULL AND source_generation IS NOT NULL AND second_build IS NULL)
 OR (kind IN ('mask','compatibility','comparison') AND sli_id IS NULL AND definition_revision IS NULL AND measurement_revision IS NULL AND hour_start IS NULL AND source_generation IS NULL AND (second_build IS NOT NULL)=(kind='comparison'))),
 UNIQUE NULLS NOT DISTINCT(kind,slo_id,target_revision,build_id,second_build,sli_id,definition_revision,measurement_revision,hour_start,source_generation)
);
CREATE INDEX almanac_maintenance_preparation_due_idx ON public.almanac_maintenance_preparation(priority,available_at,id) WHERE state='pending';
CREATE FUNCTION public.guard_maintenance_preparation_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.kind,NEW.slo_id,NEW.target_revision,NEW.build_id,NEW.second_build,NEW.sli_id,NEW.definition_revision,NEW.measurement_revision,NEW.hour_start,NEW.source_generation,NEW.created_at)
 IS DISTINCT FROM (OLD.id,OLD.kind,OLD.slo_id,OLD.target_revision,OLD.build_id,OLD.second_build,OLD.sli_id,OLD.definition_revision,OLD.measurement_revision,OLD.hour_start,OLD.source_generation,OLD.created_at)
 OR (OLD.state<>'pending' AND NEW.state<>OLD.state)
 OR (OLD.priority='live' AND NEW.priority<>'live') THEN
  RAISE EXCEPTION 'immutable maintenance preparation identity' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER almanac_maintenance_preparation_identity BEFORE UPDATE ON public.almanac_maintenance_preparation FOR EACH ROW EXECUTE FUNCTION public.guard_maintenance_preparation_identity();
