-- ADR-0034: derived work keeps retrying after its fifth consecutive failure (every 5 minutes for a
-- day, then hourly) and stops only after 7 days of continuous failure. failing_since records when the
-- current run of failures began; a success clears it. Attempt counts stay capped at five and keep
-- meaning "failing" for the existing unavailable flags and diagnostics.
ALTER TABLE public.almanac_derived_dirty_ranges ADD COLUMN failing_since timestamptz;
ALTER TABLE public.almanac_slo_current_failures ADD COLUMN failing_since timestamptz;
ALTER TABLE public.almanac_slo_series_repair ADD COLUMN failing_since timestamptz;
ALTER TABLE public.almanac_slo_series_tile_work ADD COLUMN failing_since timestamptz;
ALTER TABLE public.almanac_slo_history_jobs ADD COLUMN failing_since timestamptz;

-- Work that exhausted the old five-attempt budget resumes: it counts as failing since now.
UPDATE public.almanac_derived_dirty_ranges SET failing_since=now() WHERE attempts>=5;
UPDATE public.almanac_slo_current_failures SET failing_since=now() WHERE attempts>=5;
UPDATE public.almanac_slo_series_repair SET failing_since=now() WHERE attempts>=5;
UPDATE public.almanac_slo_series_tile_work SET failing_since=now() WHERE attempts>=5;
UPDATE public.almanac_slo_history_jobs SET failing_since=now()
WHERE state='failed' AND last_error IS DISTINCT FROM 'retention_expired';

-- New observations for an hour restart its aggregation from a clean failure state.
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
            updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at, EXCLUDED.updated_at),
            attempts=0, failing_since=NULL;
    -- AFTER INSERT preserves duplicate/no-op ingestion semantics. The internal
    -- watermark update does not fire this insert-only trigger again.
    UPDATE public.almanac_sli_coverage SET ingestion_generation=next_generation
    WHERE interval_start=NEW.interval_start AND sli_id=NEW.sli_id
      AND measurement_revision=NEW.measurement_revision;
    RETURN NEW;
END;
$$;
