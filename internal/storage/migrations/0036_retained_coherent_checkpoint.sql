ALTER TABLE public.almanac_slo_series_repair
    ADD COLUMN coherent_through timestamptz,
    ADD COLUMN coherent_source_generation bigint NOT NULL DEFAULT 0
        CHECK (coherent_source_generation>=0);

-- Completed repairs prove every earlier endpoint against this committed source.
UPDATE public.almanac_slo_series_repair w
SET coherent_through=w.cursor_at,coherent_source_generation=coalesce(g.generation,0)
FROM public.almanac_slo_definitions d
LEFT JOIN public.almanac_sli_source_generations g
    ON g.sli_id=d.sli_id AND g.measurement_revision=d.measurement_revision
WHERE d.slo_id=w.slo_id AND d.revision=w.definition_revision
  AND d.measurement_revision=w.measurement_revision AND w.cursor_at>=w.end_at;
