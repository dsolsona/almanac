CREATE TABLE public.almanac_alert_breach_pins (
 occurrence_id uuid PRIMARY KEY,policy_id uuid NOT NULL,revision bigint NOT NULL,
 evaluation_id uuid NOT NULL,
 FOREIGN KEY(policy_id,revision,occurrence_id) REFERENCES public.almanac_alert_occurrences(policy_id,revision,id),
 FOREIGN KEY(policy_id,revision,evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id)
);
INSERT INTO public.almanac_alert_breach_pins(occurrence_id,policy_id,revision,evaluation_id)
SELECT o.id,o.policy_id,o.revision,e.id FROM public.almanac_alert_occurrences o
JOIN public.almanac_alert_evaluations e ON (e.policy_id,e.revision)=(o.policy_id,o.revision)
AND e.truth='breaching' AND e.evidence->'Evaluation'->'Snapshot'=o.payload->'LatestBreachingSnapshot';
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.almanac_alert_occurrences o LEFT JOIN public.almanac_alert_breach_pins p ON p.occurrence_id=o.id WHERE p.occurrence_id IS NULL)
 THEN RAISE EXCEPTION 'alert last-breaching evidence is missing'; END IF;
END $$;
CREATE INDEX almanac_alert_breach_pin_evaluation_idx ON public.almanac_alert_breach_pins(evaluation_id);
CREATE INDEX almanac_alert_firing_evaluation_idx ON public.almanac_alert_occurrences(firing_evaluation_id);
CREATE INDEX almanac_alert_latest_evaluation_idx ON public.almanac_alert_occurrences(latest_evaluation_id);
CREATE INDEX almanac_alert_transition_evaluation_idx ON public.almanac_alert_transitions(evaluation_id) WHERE evaluation_id IS NOT NULL;
CREATE INDEX almanac_alert_cursor_evaluation_idx ON public.almanac_alert_cursors(last_evaluation_id) WHERE last_evaluation_id IS NOT NULL;
CREATE INDEX almanac_alert_range_evaluation_idx ON public.almanac_alert_ranges(reference_evaluation_id) WHERE reference_evaluation_id IS NOT NULL;
CREATE INDEX almanac_alert_terminal_retention_idx ON public.almanac_alert_occurrences(updated_at,id) WHERE state IN ('resolved','disabled','superseded');
CREATE INDEX almanac_alert_range_retention_idx ON public.almanac_alert_ranges(recorded_at,id);
ALTER TABLE public.almanac_alert_ranges DROP CONSTRAINT almanac_alert_ranges_kind_check;
ALTER TABLE public.almanac_alert_ranges ADD CONSTRAINT almanac_alert_ranges_kind_check CHECK(kind IN ('repeated_cutoff','unreconstructed_gap','administrative_cancellation','compacted_evaluations'));
ALTER TABLE public.almanac_alert_ranges ADD COLUMN summary jsonb,
 ADD CONSTRAINT almanac_alert_range_summary CHECK((kind='compacted_evaluations')=(summary IS NOT NULL) AND (summary IS NULL OR jsonb_typeof(summary)='object'));

-- Keyset over raw rows before eligibility/pin filtering bounds examined work and
-- prevents old pinned/recent rows from starving later policies indefinitely.
CREATE TABLE public.almanac_alert_retention_work (
 kind text PRIMARY KEY CHECK(kind IN ('evaluation','occurrence','range')),
 after_policy uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 after_revision bigint NOT NULL DEFAULT 0,
 after_time timestamptz NOT NULL DEFAULT '-infinity',
 after_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 version bigint NOT NULL DEFAULT 1 CHECK(version>0)
);
INSERT INTO public.almanac_alert_retention_work(kind) VALUES('evaluation'),('occurrence'),('range');
