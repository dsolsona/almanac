CREATE INDEX almanac_sli_latest_accepted_receipt_idx
    ON public.almanac_sli_coverage (sli_id, recorded_at DESC, interval_start)
    WHERE outcome IN ('observed', 'complete_empty');

CREATE INDEX almanac_slo_definitions_sli_idx
    ON public.almanac_slo_definitions (sli_id, revision, slo_id);
