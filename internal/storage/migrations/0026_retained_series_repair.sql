-- A persistent cursor survives response-cache expiry and process restarts.
-- Completed cursors stay present so ordinary publication only extends their tail.
CREATE TABLE public.almanac_slo_series_repair (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    calculation_version text NOT NULL,
    cursor_at timestamptz NOT NULL,
    end_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    next_attempt_at timestamptz NOT NULL,
    last_error text,
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,calculation_version),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (cursor_at<=end_at)
);
CREATE INDEX almanac_slo_series_repair_due_idx
    ON public.almanac_slo_series_repair(next_attempt_at,slo_id)
    WHERE cursor_at<end_at AND attempts<5;

-- Invalidation is committed with canonical coverage. It must not depend on a
-- source worker successfully acknowledging its separate aggregate work.
CREATE FUNCTION public.almanac_invalidate_slo_series() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- Current publication creates the initial full-history cursor. An arrival
    -- before that publication is included by the bootstrap, so no insert or
    -- application calculation-version constant is needed here.
    UPDATE public.almanac_slo_series_repair w
    SET cursor_at=LEAST(w.cursor_at,NEW.interval_start),
        end_at=GREATEST(w.end_at,to_timestamp(floor(extract(epoch FROM NEW.recorded_at)/d.cadence_seconds)*d.cadence_seconds)),
        version=w.version+1,next_attempt_at=LEAST(w.next_attempt_at,NEW.recorded_at)
    FROM public.almanac_slo_definitions d
    JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
    WHERE d.sli_id=NEW.sli_id AND d.measurement_revision=NEW.measurement_revision
      AND w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_slo_series_invalidation
AFTER INSERT ON public.almanac_sli_coverage
FOR EACH ROW EXECUTE FUNCTION public.almanac_invalidate_slo_series();
