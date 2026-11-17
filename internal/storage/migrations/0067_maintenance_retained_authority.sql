-- Preserve canonical rows and immutable requested-authority containers together.
-- Per-mark replay inputs retain their actual computed revision and source facts.
ALTER TABLE public.almanac_slo_extrema
    DROP CONSTRAINT almanac_slo_extrema_exclusion_revision_check,
    ADD CONSTRAINT almanac_slo_extrema_exclusion_revision_check CHECK (exclusion_revision>=0);

ALTER TABLE public.almanac_slo_extrema_cursor
    ADD COLUMN exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision>=0);

-- Seek the newest candidate for one exact bucket. The old primary key places
-- exclusion revision before the bucket and cannot bound this candidate lookup.
CREATE INDEX almanac_slo_extrema_authority_candidate_idx ON public.almanac_slo_extrema
    (slo_id,definition_revision,measurement_revision,calculation_version,
     resolution_seconds,range_start,exclusion_revision DESC);

-- Reuse the durable preparation queue for independently addressable retained
-- buckets. Existing natural keys, terminal-state guards, leases and retries stay
-- unchanged; no distant buckets are coalesced into a history cursor envelope.
ALTER TABLE public.almanac_maintenance_preparation
 DROP CONSTRAINT almanac_maintenance_preparation_kind_check,
 DROP CONSTRAINT almanac_maintenance_preparation_check2,
 ADD CONSTRAINT almanac_maintenance_preparation_kind_check
 CHECK(kind IN ('mask','compatibility','scoped_hour','comparison','extrema_hour','extrema_day')),
 ADD CONSTRAINT almanac_maintenance_preparation_check2
 CHECK((kind IN ('scoped_hour','extrema_hour','extrema_day') AND sli_id IS NOT NULL AND definition_revision IS NOT NULL AND measurement_revision IS NOT NULL AND hour_start IS NOT NULL AND source_generation IS NOT NULL AND second_build IS NULL)
 OR (kind IN ('mask','compatibility','comparison') AND sli_id IS NULL AND definition_revision IS NULL AND measurement_revision IS NULL AND hour_start IS NULL AND source_generation IS NULL AND (second_build IS NOT NULL)=(kind='comparison'))),
 ADD CONSTRAINT almanac_maintenance_preparation_extrema_bucket_check
 CHECK(kind NOT IN ('extrema_hour','extrema_day') OR (
 hour_start >= timestamptz '1970-01-01 00:00:00+00'
 AND hour_start + CASE WHEN kind='extrema_day' THEN interval '24 hours' ELSE interval '1 hour' END <= timestamptz '9999-12-31 23:59:59.999999+00'
 AND hour_start AT TIME ZONE 'UTC' = date_trunc(CASE WHEN kind='extrema_day' THEN 'day' ELSE 'hour' END, hour_start AT TIME ZONE 'UTC')));
