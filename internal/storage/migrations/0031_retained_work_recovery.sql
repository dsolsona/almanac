ALTER TABLE public.almanac_slo_series_repair
    ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    ADD COLUMN manual_retried_at timestamptz;
