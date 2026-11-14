-- Durable processor health is separate from numerical SLO evidence.
ALTER TABLE public.almanac_alert_cursors
 ADD COLUMN failure_attempts integer NOT NULL DEFAULT 0 CHECK(failure_attempts>=0),
 ADD COLUMN failure_since timestamptz,
 ADD COLUMN last_failure_at timestamptz,
 ADD COLUMN exhausted boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT almanac_alert_failure_state CHECK(
   (failure_attempts=0 AND failure_since IS NULL AND last_failure_at IS NULL AND NOT exhausted)
   OR (failure_attempts>0 AND failure_since IS NOT NULL AND last_failure_at IS NOT NULL AND last_failure_at>=failure_since)
 );
