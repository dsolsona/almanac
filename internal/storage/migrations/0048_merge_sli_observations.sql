-- Upgrade copies and verifies each frozen legacy partition in bounded committed
-- batches before applying this final migration. A failed copy leaves both old tables.
SET LOCAL statement_timeout='5s';
DROP TABLE public.almanac_sli_observation_values;
DROP TABLE public.almanac_sli_coverage;
DROP FUNCTION public.almanac_mark_derived_dirty();
DROP FUNCTION public.almanac_invalidate_slo_series();
