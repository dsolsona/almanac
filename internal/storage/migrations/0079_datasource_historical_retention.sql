ALTER TABLE public.almanac_datasources
    ADD COLUMN historical_retention_days integer NOT NULL DEFAULT 0
        CHECK (historical_retention_days BETWEEN 0 AND 36500);
