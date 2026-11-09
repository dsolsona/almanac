CREATE TABLE public.almanac_slo_extrema (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL CHECK (measurement_revision>0),
    exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision=0),
    calculation_version text NOT NULL,
    resolution_seconds integer NOT NULL CHECK (resolution_seconds IN (3600,86400)),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation>=0),
    retention_start timestamptz NOT NULL,
    published_at timestamptz NOT NULL,
    lanes jsonb NOT NULL CHECK (jsonb_typeof(lanes)='object'),
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (range_end=range_start+resolution_seconds*interval '1 second'),
    CHECK (mod(extract(epoch FROM range_start),resolution_seconds)=0)
);
CREATE INDEX almanac_slo_extrema_retention_idx ON public.almanac_slo_extrema(range_end);

CREATE TABLE public.almanac_slo_extrema_cursor (
    id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL CHECK (measurement_revision>0),
    calculation_version text NOT NULL,
    built_through timestamptz NOT NULL,
    dirty_from timestamptz,
    dirty_to timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    next_attempt_at timestamptz NOT NULL,
    last_error text,
    failing_since timestamptz,
    manual_retried_at timestamptz,
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,calculation_version),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK ((dirty_from IS NULL AND dirty_to IS NULL) OR
           (dirty_from IS NOT NULL AND dirty_to IS NOT NULL AND dirty_from<dirty_to)),
    CHECK (mod(extract(epoch FROM built_through),3600)=0)
);
CREATE INDEX almanac_slo_extrema_cursor_due_idx ON public.almanac_slo_extrema_cursor(next_attempt_at,slo_id);

INSERT INTO public.almanac_slo_extrema_cursor
    (slo_id,definition_revision,measurement_revision,calculation_version,built_through,next_attempt_at)
SELECT d.slo_id,d.revision,d.measurement_revision,'occurrences-operational-v1',
       date_trunc('hour',GREATEST(d.evaluation_valid_from,m.effective_from,now()-interval '365 days') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',now()
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision;

-- Both representations remain maintained until the final consumers switch.
CREATE OR REPLACE FUNCTION public.almanac_invalidate_slo_series() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.almanac_slo_series_repair w
    SET cursor_at=LEAST(w.cursor_at,NEW.interval_start),
        end_at=GREATEST(w.end_at,to_timestamp(floor(extract(epoch FROM NEW.recorded_at)/d.cadence_seconds)*d.cadence_seconds)),
        version=w.version+1,next_attempt_at=LEAST(w.next_attempt_at,NEW.recorded_at)
    FROM public.almanac_slo_definitions d
    JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
    WHERE d.sli_id=NEW.sli_id AND d.measurement_revision=NEW.measurement_revision
      AND w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;

    UPDATE public.almanac_slo_extrema_cursor w
    SET dirty_from=LEAST(w.dirty_from,date_trunc('hour',NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
        dirty_to=GREATEST(w.dirty_to,(date_trunc('hour',NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
            +(GREATEST(d.rolling_window_seconds,21600)+3600)*interval '1 second'),
        version=w.version+1,attempts=0,failing_since=NULL,last_error=NULL,
        next_attempt_at=LEAST(w.next_attempt_at,NEW.recorded_at)
    FROM public.almanac_slo_definitions d
    JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
    WHERE d.sli_id=NEW.sli_id AND d.measurement_revision=NEW.measurement_revision
      AND w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;
    RETURN NEW;
END;
$$;
