-- Unwired evaluator candidate seeks must not walk incomplete revision histories.
CREATE INDEX almanac_maintenance_complete_candidate_idx
 ON public.almanac_maintenance_builds(slo_id,target_revision DESC)
 WHERE phase='complete';
-- Exact build lookups retain the primary key. This supports at most one older
-- prepared population for this same immutable definition and physical source.
CREATE INDEX almanac_maintenance_hour_candidate_idx
 ON public.almanac_slo_maintenance_hours
 (slo_id,definition_revision,measurement_revision,hour_start,source_generation,exclusion_revision DESC);
