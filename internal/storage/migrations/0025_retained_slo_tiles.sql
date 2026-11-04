CREATE TABLE public.almanac_slo_series_tiles (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision>=0),
    calculation_version text NOT NULL,
    resolution_seconds integer NOT NULL CHECK (resolution_seconds IN (3600,86400)),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation>=0),
    point_count integer NOT NULL CHECK (point_count BETWEEN 0 AND 5760),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    published_at timestamptz NOT NULL,
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (range_end=range_start+resolution_seconds*interval '1 second')
);
CREATE INDEX almanac_slo_series_tiles_retention_idx ON public.almanac_slo_series_tiles(range_end);

CREATE TABLE public.almanac_slo_series_tile_work (
    id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision>=0),
    calculation_version text NOT NULL,
    resolution_seconds integer NOT NULL CHECK (resolution_seconds IN (3600,86400)),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    source_generation bigint NOT NULL CHECK (source_generation>=0),
    next_attempt_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    last_error text,
    manual_retried_at timestamptz,
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (range_end=range_start+resolution_seconds*interval '1 second')
);
CREATE INDEX almanac_slo_series_tile_work_due_idx
    ON public.almanac_slo_series_tile_work(next_attempt_at,resolution_seconds,range_start) WHERE attempts<5;
