-- The active grid inventory keeps selector publication independent of the
-- number of SLOs matching a selector. Service and installation counts are
-- maintained with the same authority lock as prospective assignments.
LOCK TABLE public.almanac_services, public.almanac_slos IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE public.almanac_maintenance_service_grids (
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    cadence_seconds integer NOT NULL,
    slo_count bigint NOT NULL CHECK (slo_count >= 0),
    PRIMARY KEY (service_id, cadence_seconds)
);

CREATE TABLE public.almanac_maintenance_installation_grids (
    cadence_seconds integer PRIMARY KEY,
    slo_count bigint NOT NULL CHECK (slo_count >= 0)
);

INSERT INTO public.almanac_maintenance_service_grids (service_id, cadence_seconds, slo_count)
SELECT d.service_id, d.cadence_seconds, count(*)
FROM public.almanac_slos slo
JOIN public.almanac_slo_definitions d ON d.slo_id = slo.id AND d.revision = slo.definition_revision
GROUP BY d.service_id, d.cadence_seconds;

INSERT INTO public.almanac_maintenance_installation_grids (cadence_seconds, slo_count)
SELECT g.cadence_seconds, sum(g.slo_count)
FROM public.almanac_maintenance_service_grids g
JOIN public.almanac_services s ON s.id = g.service_id
WHERE s.archived_at IS NULL
GROUP BY g.cadence_seconds;

CREATE FUNCTION public.almanac_adjust_maintenance_installation_grid(cadence integer, delta bigint)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF delta > 0 THEN
        INSERT INTO public.almanac_maintenance_installation_grids (cadence_seconds, slo_count)
        VALUES (cadence, delta)
        ON CONFLICT (cadence_seconds) DO UPDATE SET slo_count =
            almanac_maintenance_installation_grids.slo_count + excluded.slo_count;
    ELSE
        UPDATE public.almanac_maintenance_installation_grids
        SET slo_count = slo_count + delta WHERE cadence_seconds = cadence;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'maintenance installation grid was not counted' USING ERRCODE = '23514';
        END IF;
        DELETE FROM public.almanac_maintenance_installation_grids
        WHERE cadence_seconds = cadence AND slo_count = 0;
    END IF;
END $$;

CREATE FUNCTION public.almanac_adjust_maintenance_service_grid(subject uuid, cadence integer, delta bigint)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF delta > 0 THEN
        INSERT INTO public.almanac_maintenance_service_grids (service_id, cadence_seconds, slo_count)
        VALUES (subject, cadence, delta)
        ON CONFLICT (service_id, cadence_seconds) DO UPDATE SET slo_count =
            almanac_maintenance_service_grids.slo_count + excluded.slo_count;
    ELSE
        UPDATE public.almanac_maintenance_service_grids
        SET slo_count = slo_count + delta WHERE service_id = subject AND cadence_seconds = cadence;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'maintenance service grid was not counted' USING ERRCODE = '23514';
        END IF;
        DELETE FROM public.almanac_maintenance_service_grids
        WHERE service_id = subject AND cadence_seconds = cadence AND slo_count = 0;
    END IF;

    IF (SELECT archived_at IS NULL FROM public.almanac_services WHERE id = subject) THEN
        PERFORM public.almanac_adjust_maintenance_installation_grid(cadence, delta);
    END IF;
END $$;

CREATE FUNCTION public.almanac_record_maintenance_grid() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    previous_grid record;
    next_grid record;
BEGIN
    SELECT service_id, cadence_seconds INTO STRICT next_grid
    FROM public.almanac_slo_definitions WHERE slo_id = NEW.id AND revision = NEW.definition_revision;
    IF TG_OP = 'UPDATE' THEN
        SELECT service_id, cadence_seconds INTO STRICT previous_grid
        FROM public.almanac_slo_definitions WHERE slo_id = OLD.id AND revision = OLD.definition_revision;
        IF previous_grid = next_grid THEN
            RETURN NULL;
        END IF;
    END IF;

    PERFORM revision FROM public.almanac_service_maintenance_authority WHERE singleton FOR UPDATE;
    IF TG_OP = 'UPDATE' THEN
        PERFORM public.almanac_adjust_maintenance_service_grid(previous_grid.service_id, previous_grid.cadence_seconds, -1);
    END IF;
    PERFORM public.almanac_adjust_maintenance_service_grid(next_grid.service_id, next_grid.cadence_seconds, 1);
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER almanac_slo_grid_initial
    AFTER INSERT ON public.almanac_slos
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.almanac_record_maintenance_grid();
CREATE CONSTRAINT TRIGGER almanac_slo_grid_changed
    AFTER UPDATE OF definition_revision ON public.almanac_slos
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (OLD.definition_revision IS DISTINCT FROM NEW.definition_revision)
    EXECUTE FUNCTION public.almanac_record_maintenance_grid();

CREATE FUNCTION public.almanac_record_maintenance_grid_service_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    grid record;
BEGIN
    PERFORM revision FROM public.almanac_service_maintenance_authority WHERE singleton FOR UPDATE;
    FOR grid IN SELECT cadence_seconds, slo_count FROM public.almanac_maintenance_service_grids WHERE service_id = NEW.id
    LOOP
        PERFORM public.almanac_adjust_maintenance_installation_grid(
            grid.cadence_seconds, CASE WHEN NEW.archived_at IS NULL THEN grid.slo_count ELSE -grid.slo_count END
        );
    END LOOP;
    RETURN NULL;
END $$;
CREATE TRIGGER almanac_maintenance_grid_service_state
    AFTER UPDATE OF archived_at ON public.almanac_services FOR EACH ROW
    WHEN ((OLD.archived_at IS NULL) <> (NEW.archived_at IS NULL))
    EXECUTE FUNCTION public.almanac_record_maintenance_grid_service_state();
