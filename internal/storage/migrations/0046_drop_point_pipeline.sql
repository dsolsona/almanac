-- Canonical acceptance still invalidates the extrema cursor in the same transaction.
-- Preserve its version and lease-epoch fences from migration 0045.
CREATE OR REPLACE FUNCTION public.almanac_invalidate_slo_series() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.almanac_slo_extrema_cursor w
    SET dirty_from=LEAST(w.dirty_from,date_trunc('hour',NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
        dirty_to=GREATEST(w.dirty_to,(date_trunc('hour',NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
            +(GREATEST(d.rolling_window_seconds,21600)+3600)*interval '1 second'),
        version=w.version+1,
        attempts=CASE WHEN w.failing_since<=NEW.recorded_at-interval '7 days' THEN 0 ELSE w.attempts END,
        last_error=CASE WHEN w.failing_since<=NEW.recorded_at-interval '7 days' THEN NULL ELSE w.last_error END,
        failing_since=CASE WHEN w.failing_since<=NEW.recorded_at-interval '7 days' THEN NULL ELSE w.failing_since END,
        next_attempt_at=CASE WHEN w.failing_since IS NOT NULL AND w.failing_since>NEW.recorded_at-interval '7 days'
            THEN w.next_attempt_at ELSE LEAST(w.next_attempt_at,NEW.recorded_at) END
    FROM public.almanac_slo_definitions d
    JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
    WHERE d.sli_id=NEW.sli_id AND d.measurement_revision=NEW.measurement_revision
      AND w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;
    RETURN NEW;
END;
$$;

DROP TABLE public.almanac_slo_series_points;
DROP TABLE public.almanac_slo_series_tiles;
DROP TABLE public.almanac_slo_series_tile_work;
DROP TABLE public.almanac_slo_series_repair;
DROP TABLE public.almanac_slo_display_blocks;
DROP TABLE public.almanac_slo_history_jobs;
DROP TABLE public.almanac_slo_history_admission;
DROP FUNCTION public.almanac_retained_tile_digest();

-- Only pinned exact-value payloads consume the derived history cache budget now.
CREATE OR REPLACE FUNCTION public.almanac_account_derived_cache() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE change_bytes bigint := 0;
BEGIN
    IF TG_OP<>'DELETE' THEN change_bytes:=coalesce(NEW.payload_bytes,octet_length(NEW.payload::text)); END IF;
    IF TG_OP<>'INSERT' THEN change_bytes:=change_bytes-coalesce(OLD.payload_bytes,octet_length(OLD.payload::text)); END IF;
    IF change_bytes<>0 THEN
        UPDATE public.almanac_derived_cache_usage SET payload_bytes=payload_bytes+change_bytes
        WHERE singleton AND payload_bytes+change_bytes BETWEEN 0 AND 2147483648;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='54000',MESSAGE='derived cache budget exhausted';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
LOCK TABLE public.almanac_slo_history_blocks IN SHARE ROW EXCLUSIVE MODE;
UPDATE public.almanac_derived_cache_usage
SET payload_bytes=(SELECT coalesce(sum(coalesce(payload_bytes,octet_length(payload::text))),0)
                   FROM public.almanac_slo_history_blocks)
WHERE singleton;

-- Archive keys retain their existing identity scheme until overview snapshots leave in F9-T05.
-- Their hashes identify immutable rows; integrity checks remain only on history_blocks.
ALTER TABLE public.almanac_slo_projection_payloads DROP CONSTRAINT almanac_slo_projection_payloads_check;
CREATE OR REPLACE FUNCTION public.almanac_archive_slo_projection() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.almanac_slo_projection_payloads(digest,payload,urgency,payload_bytes,published_at)
    SELECT NEW.payload_digest,NEW.payload,NEW.urgency,octet_length(NEW.payload::text),g.published_at
    FROM public.almanac_slo_derived_generations g WHERE g.id=NEW.generation_id
    ON CONFLICT (digest) DO NOTHING;
    RETURN NEW;
END;
$$;
