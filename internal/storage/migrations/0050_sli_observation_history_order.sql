-- Latest intervals and history pages span measurement revisions.
CREATE INDEX IF NOT EXISTS almanac_sli_observations_history_idx
ON public.almanac_sli_observations(sli_id,interval_start DESC,recorded_at DESC);
