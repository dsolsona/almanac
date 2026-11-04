-- Time-range reads must not scan past ingestion-generation keys for each edge.
CREATE INDEX almanac_sli_coverage_revision_time_idx
    ON public.almanac_sli_coverage(sli_id,measurement_revision,interval_start);
