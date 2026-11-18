-- Suppressed openings have no occurrence to join. Index the immutable
-- evaluation evidence directly so a policy audit visits only suppressed rows.
CREATE INDEX almanac_alert_maintenance_suppression_audit_idx
    ON public.almanac_alert_evaluations (policy_id, revision, effective_cutoff DESC, id DESC)
    WHERE evidence @> '{"Evaluation":{"Maintenance":{"OpeningSuppressed":true}}}'::jsonb;
