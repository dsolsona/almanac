ALTER TABLE public.almanac_datasources
    ADD COLUMN historical_retention_days integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT almanac_datasource_historical_retention_bounds
        CHECK (historical_retention_days BETWEEN 0 AND 36500) NOT VALID;
