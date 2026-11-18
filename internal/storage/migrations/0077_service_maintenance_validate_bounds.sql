-- Validate after the metadata-only constraint changes commit. Validation
-- uses SHARE UPDATE EXCLUSIVE, allowing ordinary reads and writes.
ALTER TABLE public.almanac_service_maintenance_membership_work
    VALIDATE CONSTRAINT almanac_service_maintenance_affected_from_finite;
ALTER TABLE public.almanac_maintenance_change_sets
    VALIDATE CONSTRAINT almanac_maintenance_change_sets_range_count_check;
ALTER TABLE public.almanac_maintenance_changed_inputs
    VALIDATE CONSTRAINT almanac_maintenance_changed_inputs_part_check;
ALTER TABLE public.almanac_slo_exclusion_work
    VALIDATE CONSTRAINT almanac_slo_exclusion_work_progress_part_check;
