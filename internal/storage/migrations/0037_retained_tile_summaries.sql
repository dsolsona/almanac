ALTER TABLE public.almanac_slo_series_tiles
    ADD COLUMN payload_digest bytea,
    ADD COLUMN compact_payload jsonb CHECK (compact_payload IS NULL OR
      (jsonb_typeof(compact_payload)='object' AND (NOT compact_payload ? 'tile' OR jsonb_typeof(compact_payload->'tile')='object')
       AND jsonb_typeof(compact_payload->'transitions')='array' AND jsonb_array_length(compact_payload->'transitions')=4));

CREATE FUNCTION public.almanac_retained_tile_digest() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    NEW.payload_digest := sha256(convert_to(NEW.payload::text,'UTF8'));
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_retained_tile_digest BEFORE INSERT OR UPDATE OF payload
ON public.almanac_slo_series_tiles FOR EACH ROW EXECUTE FUNCTION public.almanac_retained_tile_digest();

ALTER TABLE public.almanac_slo_series_tile_work ADD COLUMN summary_only boolean NOT NULL DEFAULT false;
CREATE INDEX almanac_slo_series_tile_summary_idx
ON public.almanac_slo_series_tiles(resolution_seconds DESC,range_end DESC,slo_id)
WHERE compact_payload IS NULL OR payload_digest IS NULL
   OR compact_payload->>'payload_digest' IS DISTINCT FROM encode(payload_digest,'hex')
   OR compact_payload->>'source_generation' IS DISTINCT FROM source_generation::text;
CREATE INDEX almanac_slo_series_tile_work_priority_idx
ON public.almanac_slo_series_tile_work(summary_only,next_attempt_at,resolution_seconds,range_start DESC,id) WHERE attempts<5;
