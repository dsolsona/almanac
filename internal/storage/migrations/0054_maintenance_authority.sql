-- Internal authority only; public routes and production calculation adoption are separate.
CREATE TABLE public.almanac_maintenance_exclusions (
 id uuid PRIMARY KEY, current_revision bigint NOT NULL CHECK (current_revision>0),
 creator_id uuid NOT NULL REFERENCES public.almanac_users(id), created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE public.almanac_maintenance_exclusion_revisions (
 exclusion_id uuid NOT NULL REFERENCES public.almanac_maintenance_exclusions(id), revision bigint NOT NULL CHECK (revision>0),
 title text NOT NULL CHECK (octet_length(btrim(title)) BETWEEN 1 AND 512),
 reason text NOT NULL CHECK (octet_length(btrim(reason)) BETWEEN 1 AND 4096),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 operation text NOT NULL CHECK (operation IN ('create','edit','end','cancel','withdraw')), tombstone boolean NOT NULL,
 actor_id uuid NOT NULL REFERENCES public.almanac_users(id), recorded_at timestamptz NOT NULL,
 request_id text NOT NULL CHECK (octet_length(request_id)>0), audit_id uuid NOT NULL REFERENCES public.almanac_audit_events(id),
 PRIMARY KEY (exclusion_id,revision),
 CHECK (isfinite(starts_at) AND isfinite(ends_at) AND starts_at<ends_at),
 CHECK (tombstone=(operation IN ('cancel','withdraw')))
);
ALTER TABLE public.almanac_maintenance_exclusions ADD CONSTRAINT almanac_maintenance_current_revision_fk
 FOREIGN KEY (id,current_revision) REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id,revision) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE public.almanac_maintenance_exclusion_scopes (
 exclusion_id uuid NOT NULL, revision bigint NOT NULL, slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
 PRIMARY KEY (exclusion_id,revision,slo_id), FOREIGN KEY (exclusion_id,revision) REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id,revision)
);
CREATE INDEX almanac_maintenance_scope_slo_idx ON public.almanac_maintenance_exclusion_scopes(slo_id,exclusion_id,revision);
CREATE INDEX almanac_maintenance_page_idx ON public.almanac_maintenance_exclusions(created_at,id);
CREATE TABLE public.almanac_slo_exclusion_state (
 slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id), revision bigint NOT NULL DEFAULT 0 CHECK (revision>=0), changed_at timestamptz NOT NULL
);
INSERT INTO public.almanac_slo_exclusion_state(slo_id,changed_at) SELECT id,created_at FROM public.almanac_slos;
CREATE FUNCTION public.almanac_initialize_slo_exclusion_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO public.almanac_slo_exclusion_state(slo_id,changed_at) VALUES (NEW.id,NEW.created_at);
 RETURN NEW;
END $$;
CREATE TRIGGER almanac_slo_exclusion_state_insert AFTER INSERT ON public.almanac_slos FOR EACH ROW EXECUTE FUNCTION public.almanac_initialize_slo_exclusion_state();
CREATE TABLE public.almanac_slo_exclusion_changes (
 slo_id uuid NOT NULL REFERENCES public.almanac_slos(id), revision bigint NOT NULL CHECK (revision>0),
 exclusion_id uuid NOT NULL, exclusion_revision bigint NOT NULL, included boolean NOT NULL, coverage_changed boolean NOT NULL, recorded_at timestamptz NOT NULL,
 PRIMARY KEY (slo_id,revision), FOREIGN KEY (exclusion_id,exclusion_revision) REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id,revision)
);
CREATE INDEX almanac_slo_exclusion_snapshot_idx ON public.almanac_slo_exclusion_changes(slo_id,exclusion_id,revision DESC);
CREATE TABLE public.almanac_slo_exclusion_work (
 slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id), desired_revision bigint NOT NULL CHECK (desired_revision>0), work_version bigint NOT NULL CHECK (work_version>0),
 affected_start timestamptz, affected_end timestamptz, next_attempt_at timestamptz NOT NULL,
 CHECK ((affected_start IS NULL)=(affected_end IS NULL)), CHECK (affected_start<affected_end)
);
-- Forbid rewriting provenance, but allow future bounded retention DELETE of
-- unreferenced history after >=400 days. Foreign keys protect live references.
CREATE FUNCTION public.almanac_immutable_maintenance_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'maintenance provenance is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER almanac_maintenance_revision_immutable BEFORE UPDATE ON public.almanac_maintenance_exclusion_revisions FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();
CREATE TRIGGER almanac_maintenance_scope_immutable BEFORE UPDATE ON public.almanac_maintenance_exclusion_scopes FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();
CREATE TRIGGER almanac_maintenance_change_immutable BEFORE UPDATE ON public.almanac_slo_exclusion_changes FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();
