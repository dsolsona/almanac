DROP INDEX public.almanac_slo_series_tile_summary_idx;
CREATE INDEX almanac_slo_series_tile_summary_idx
ON public.almanac_slo_series_tiles(resolution_seconds DESC,range_end DESC,slo_id)
WHERE compact_payload IS NULL OR payload_digest IS NULL
   OR compact_payload->>'payload_digest' IS DISTINCT FROM encode(payload_digest,'hex')
   OR compact_payload->>'source_generation' IS DISTINCT FROM source_generation::text
   OR (compact_payload ? 'tile' AND NOT compact_payload ? 'wire');
