-- Receipt metadata is optional; failed/incomplete rows do not contribute to it.
-- Existing numeric summaries and source generations remain unchanged. Legacy
-- receipts stay quarantined until bounded repair certifies their provenance.
ALTER TABLE public.almanac_sli_hourly_aggregates
 ALTER COLUMN latest_receipt_at DROP NOT NULL,
 ADD COLUMN receipt_source_generation bigint,
 ADD COLUMN certified_receipt_at timestamptz;

-- Bind the certificate to both values old publishers can overwrite. A flag alone
-- would remain incorrectly trusted after an older ON CONFLICT publication.
CREATE INDEX almanac_canonical_receipt_repair_idx
 ON public.almanac_sli_hourly_aggregates(hour_start DESC,sli_id,measurement_revision)
 WHERE receipt_source_generation IS DISTINCT FROM source_generation
    OR certified_receipt_at IS DISTINCT FROM latest_receipt_at;
