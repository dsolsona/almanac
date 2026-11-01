-- Advancing this singleton's MVCC row version fences history admission even
-- when a caller established a repeatable-read snapshot before waiting. A stale
-- contender must retry its transaction instead of inserting from stale counts.
CREATE TABLE public.almanac_slo_history_admission (
    singleton boolean PRIMARY KEY CHECK (singleton)
);
INSERT INTO public.almanac_slo_history_admission(singleton) VALUES (true);
