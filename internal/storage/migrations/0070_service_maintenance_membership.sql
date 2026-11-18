-- Service maintenance uses assignment history, rather than reconstructing
-- elapsed coverage from the current SLO definition. Recording a new assignment
-- also advances the authority read by occurrence and calculation transactions.
-- Keep current assignments stable until the deferred triggers are installed.
LOCK TABLE public.almanac_slos IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE public.almanac_service_maintenance_authority (
    singleton boolean PRIMARY KEY CHECK (singleton),
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0)
);
INSERT INTO public.almanac_service_maintenance_authority(singleton) VALUES (true);

CREATE TABLE public.almanac_slo_service_assignments (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    effective_at timestamptz NOT NULL CHECK (isfinite(effective_at)),
    PRIMARY KEY (slo_id, definition_revision),
    FOREIGN KEY (slo_id, definition_revision) REFERENCES public.almanac_slo_definitions(slo_id, revision)
);
CREATE INDEX almanac_slo_service_assignment_boundary_idx
    ON public.almanac_slo_service_assignments(slo_id, effective_at DESC, definition_revision DESC);
CREATE INDEX almanac_service_assignment_inventory_idx
    ON public.almanac_slo_service_assignments(service_id, slo_id, effective_at DESC);

-- No service-scoped window can predate this migration. Seed the assignment in
-- force at upgrade. Future assignments are recorded only when actually selected.
INSERT INTO public.almanac_slo_service_assignments(slo_id, definition_revision, service_id, effective_at)
SELECT s.id, d.revision, d.service_id, GREATEST(s.created_at, d.activated_at)
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id = s.id AND d.revision = s.definition_revision;

CREATE FUNCTION public.almanac_record_active_slo_service() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    subject_id uuid;
    current_assignment record;
    prior_assignment record;
BEGIN
    subject_id := NEW.id;

    SELECT d.revision, d.service_id, d.cadence_seconds, GREATEST(s.updated_at, d.activated_at) AS effective_at
    INTO STRICT current_assignment
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id = s.id AND d.revision = s.definition_revision
    WHERE s.id = subject_id;

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

    UPDATE public.almanac_service_maintenance_authority SET revision = revision + 1 WHERE singleton;
    INSERT INTO public.almanac_slo_service_assignments(slo_id, definition_revision, service_id, effective_at)
    VALUES (subject_id, current_assignment.revision, current_assignment.service_id, current_assignment.effective_at);
    RETURN NULL;
END $$;

-- The SLO root references a deferred FK. Reading it at commit also supports
-- importing the definition and selecting it in either statement order.
CREATE CONSTRAINT TRIGGER almanac_slo_initial_service_assignment
    AFTER INSERT ON public.almanac_slos
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.almanac_record_active_slo_service();
CREATE CONSTRAINT TRIGGER almanac_slo_changed_service_assignment
    AFTER UPDATE OF definition_revision ON public.almanac_slos
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (OLD.definition_revision IS DISTINCT FROM NEW.definition_revision)
    EXECUTE FUNCTION public.almanac_record_active_slo_service();

CREATE TRIGGER almanac_slo_service_assignment_immutable
    BEFORE UPDATE ON public.almanac_slo_service_assignments
    FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();

CREATE FUNCTION public.almanac_fence_service_archive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.almanac_service_maintenance_authority SET revision = revision + 1 WHERE singleton;
    RETURN NULL;
END $$;
CREATE TRIGGER almanac_service_maintenance_archive
    AFTER UPDATE OF archived_at ON public.almanac_services
    FOR EACH ROW WHEN (OLD.archived_at IS DISTINCT FROM NEW.archived_at)
    EXECUTE FUNCTION public.almanac_fence_service_archive();
