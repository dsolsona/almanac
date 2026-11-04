ALTER TABLE public.almanac_sli_coverage
    ADD COLUMN ingestion_generation bigint NOT NULL DEFAULT 0 CHECK (ingestion_generation>=0);
CREATE INDEX almanac_sli_coverage_ingestion_generation_idx
    ON public.almanac_sli_coverage(sli_id,measurement_revision,ingestion_generation,interval_start);

CREATE OR REPLACE FUNCTION public.almanac_mark_derived_dirty() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE next_generation bigint;
BEGIN
    INSERT INTO public.almanac_sli_source_generations
        (sli_id, measurement_revision, generation, updated_at)
    VALUES (NEW.sli_id, NEW.measurement_revision, 1, NEW.recorded_at)
    ON CONFLICT (sli_id, measurement_revision) DO UPDATE
        SET generation=almanac_sli_source_generations.generation+1,
            updated_at=GREATEST(almanac_sli_source_generations.updated_at, EXCLUDED.updated_at)
    RETURNING generation INTO next_generation;

    INSERT INTO public.almanac_derived_dirty_ranges
        (sli_id, measurement_revision, generation, range_start, range_end, next_attempt_at, updated_at)
    VALUES (NEW.sli_id, NEW.measurement_revision, next_generation,
            (date_trunc('hour', NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
            GREATEST((date_trunc('hour', NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour', NEW.interval_end),
            NEW.recorded_at, NEW.recorded_at)
    ON CONFLICT (sli_id, measurement_revision, range_start) DO UPDATE
        SET generation=EXCLUDED.generation,
            range_start=LEAST(almanac_derived_dirty_ranges.range_start, EXCLUDED.range_start),
            range_end=GREATEST(almanac_derived_dirty_ranges.range_end, EXCLUDED.range_end),
            next_attempt_at=LEAST(almanac_derived_dirty_ranges.next_attempt_at, EXCLUDED.next_attempt_at),
            updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at, EXCLUDED.updated_at);
    -- AFTER INSERT preserves duplicate/no-op ingestion semantics. The internal
    -- watermark update does not fire this insert-only trigger again.
    UPDATE public.almanac_sli_coverage SET ingestion_generation=next_generation
    WHERE interval_start=NEW.interval_start AND sli_id=NEW.sli_id
      AND measurement_revision=NEW.measurement_revision;
    RETURN NEW;
END;
$$;
