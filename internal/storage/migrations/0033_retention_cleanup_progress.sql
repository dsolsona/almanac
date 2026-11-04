ALTER TABLE public.almanac_retention_work
    ADD COLUMN canonical_applied_at timestamptz,
    ADD COLUMN cleanup_counts jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(cleanup_counts)='object');

CREATE INDEX almanac_slo_series_tile_work_retention_idx ON public.almanac_slo_series_tile_work(range_end);
CREATE INDEX almanac_sli_hourly_retention_idx ON public.almanac_sli_hourly_aggregates(hour_start);
CREATE INDEX almanac_retention_expirable_source_idx ON public.almanac_derived_dirty_ranges(range_start,id)
    WHERE last_error IS DISTINCT FROM 'retention_expired';
