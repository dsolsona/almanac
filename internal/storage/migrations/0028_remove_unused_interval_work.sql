-- Source aggregation and retained-series invalidation are committed by coverage
-- triggers. This earlier queue has no consumer and is not canonical evidence.
DROP TABLE IF EXISTS public.almanac_sli_dirty_intervals;
