-- A tile can combine endpoints calculated at different source generations.
-- Range-local invalidation needs their oldest watermark, never their newest.
UPDATE public.almanac_slo_series_tiles t
SET source_generation=coalesce((
    SELECT min(p.source_generation)
    FROM public.almanac_slo_series_points p
    WHERE p.slo_id=t.slo_id AND p.definition_revision=t.definition_revision
      AND p.measurement_revision=t.measurement_revision AND p.exclusion_revision=t.exclusion_revision
      AND p.calculation_version=t.calculation_version
      AND p.evaluation_at>t.range_start AND p.evaluation_at<=t.range_end
),0);
