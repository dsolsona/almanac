-- Explicit ineligible scheduled work is neither a numerical result nor lost history.
ALTER TABLE public.almanac_alert_ranges DROP CONSTRAINT almanac_alert_ranges_kind_check;
ALTER TABLE public.almanac_alert_ranges ADD CONSTRAINT almanac_alert_ranges_kind_check
CHECK(kind IN ('repeated_cutoff','unreconstructed_gap','administrative_cancellation','compacted_evaluations','before_policy_activation'));
