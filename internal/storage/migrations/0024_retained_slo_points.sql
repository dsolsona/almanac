-- Historical calculations outlive expendable response generations and caches.
CREATE TABLE public.almanac_slo_series_points (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL CHECK (measurement_revision>0),
    exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision>=0),
    calculation_version text NOT NULL,
    evaluation_at timestamptz NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation>=0),
    published_at timestamptz NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=65536),
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,evaluation_at),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision)
);
CREATE INDEX almanac_slo_series_points_retention_idx
    ON public.almanac_slo_series_points(evaluation_at);
