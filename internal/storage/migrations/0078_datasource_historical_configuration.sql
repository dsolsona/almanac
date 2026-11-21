ALTER TABLE public.almanac_datasources
    ADD COLUMN historical_configuration_version bigint NOT NULL DEFAULT 1;
