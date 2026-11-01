-- Legacy response jobs use request-relative display buckets. Keep their pinned
-- exports readable without letting those payloads replace retained-point reads.
ALTER TABLE public.almanac_slo_history_blocks
    DROP CONSTRAINT almanac_slo_history_blocks_kind_check,
    ADD CONSTRAINT almanac_slo_history_blocks_kind_check
        CHECK (kind IN ('detail','overview','retained_detail'));
