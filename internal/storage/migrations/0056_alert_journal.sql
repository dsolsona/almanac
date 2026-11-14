-- Unwired backend foundations; this migration does not start alert evaluation.
CREATE TABLE public.almanac_alert_policies (
 id uuid PRIMARY KEY, name text NOT NULL UNIQUE CHECK (length(btrim(name))>0),
 slo_id uuid NOT NULL REFERENCES public.almanac_slos(id), evaluation_revision bigint NOT NULL CHECK (evaluation_revision>0),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0), enabled boolean NOT NULL DEFAULT true,
 severity text NOT NULL CHECK (severity IN ('low','medium','high')), created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE public.almanac_alert_policy_revisions (
 policy_id uuid NOT NULL REFERENCES public.almanac_alert_policies(id), revision bigint NOT NULL CHECK (revision>0),
 slo_id uuid NOT NULL, definition_revision bigint NOT NULL, calculation_version text NOT NULL CHECK (length(calculation_version)>0),
 conditions jsonb NOT NULL CHECK (jsonb_typeof(conditions)='array' AND jsonb_array_length(conditions)>0),
 created_at timestamptz NOT NULL, PRIMARY KEY(policy_id,revision), UNIQUE(policy_id,revision,slo_id),
 FOREIGN KEY(slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision)
);
ALTER TABLE public.almanac_alert_policies ADD CONSTRAINT almanac_alert_current_revision_fk
 FOREIGN KEY(id,evaluation_revision,slo_id) REFERENCES public.almanac_alert_policy_revisions(policy_id,revision,slo_id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX almanac_alert_policy_slo_idx ON public.almanac_alert_policies(slo_id,id);

CREATE TABLE public.almanac_alert_evaluations (
 id uuid PRIMARY KEY, policy_id uuid NOT NULL, revision bigint NOT NULL,
 scheduled_at timestamptz NOT NULL, effective_cutoff timestamptz NOT NULL,
 truth text NOT NULL CHECK (truth IN ('breaching','clear','indeterminate')),
 source_generation bigint NOT NULL CHECK (source_generation>=0), exclusion_revision bigint NOT NULL CHECK (exclusion_revision>=0),
 claim_epoch bigint NOT NULL CHECK (claim_epoch>0), claim_token uuid NOT NULL,
 input_digest bytea NOT NULL CHECK (octet_length(input_digest)=32),
 evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence)='object'), result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
 committed_at timestamptz NOT NULL CHECK (committed_at>=effective_cutoff),
 FOREIGN KEY(policy_id,revision) REFERENCES public.almanac_alert_policy_revisions(policy_id,revision),
 UNIQUE(policy_id,revision,effective_cutoff), UNIQUE(policy_id,revision,scheduled_at), UNIQUE(policy_id,revision,id)
);
CREATE TABLE public.almanac_alert_occurrences (
 id uuid PRIMARY KEY, policy_id uuid NOT NULL, revision bigint NOT NULL,
 state text NOT NULL CHECK (state IN ('firing','acknowledged','resolved','disabled','superseded')),
 version bigint NOT NULL CHECK (version>0), transition_sequence bigint NOT NULL CHECK (transition_sequence>0),
 firing_evaluation_id uuid NOT NULL, latest_evaluation_id uuid NOT NULL,
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND payload->>'State'=state AND payload->>'ID'=id::text AND payload->>'PolicyID'=policy_id::text),
 policy_snapshot jsonb NOT NULL CHECK (jsonb_typeof(policy_snapshot)='object'),
 opened_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, terminal_at timestamptz,
 CHECK ((state IN ('firing','acknowledged'))=(terminal_at IS NULL)),
 FOREIGN KEY(policy_id,revision,firing_evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id),
 FOREIGN KEY(policy_id,revision,latest_evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id),
 UNIQUE(policy_id,revision,id)
);
CREATE UNIQUE INDEX almanac_alert_one_open_idx ON public.almanac_alert_occurrences(policy_id) WHERE state IN ('firing','acknowledged');
CREATE INDEX almanac_alert_occurrence_state_idx ON public.almanac_alert_occurrences(state,opened_at,id);
CREATE TABLE public.almanac_alert_cursors (
 policy_id uuid NOT NULL, revision bigint NOT NULL, schedulable boolean NOT NULL DEFAULT true,
 origin_at timestamptz NOT NULL, next_scheduled_at timestamptz NOT NULL, available_at timestamptz NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), epoch bigint NOT NULL DEFAULT 0 CHECK(epoch>=0),
 claim_token uuid, claim_until timestamptz, last_attempt_tick timestamptz, last_dispatched_at timestamptz,
 last_cutoff timestamptz, last_evaluation_id uuid, open_occurrence_id uuid, waiting_reason text NOT NULL DEFAULT '',
 PRIMARY KEY(policy_id,revision), CHECK ((claim_token IS NULL)=(claim_until IS NULL)),
 CHECK (claim_token IS NULL OR epoch>0), CHECK (next_scheduled_at>=origin_at),
 FOREIGN KEY(policy_id,revision) REFERENCES public.almanac_alert_policy_revisions(policy_id,revision),
 FOREIGN KEY(policy_id,revision,last_evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id),
 FOREIGN KEY(policy_id,revision,open_occurrence_id) REFERENCES public.almanac_alert_occurrences(policy_id,revision,id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX almanac_alert_due_idx ON public.almanac_alert_cursors(available_at,last_dispatched_at,policy_id) WHERE schedulable;
CREATE INDEX almanac_alert_expired_claim_idx ON public.almanac_alert_cursors(claim_until) WHERE claim_until IS NOT NULL;
CREATE TABLE public.almanac_alert_transitions (
 id uuid PRIMARY KEY, policy_id uuid NOT NULL, revision bigint NOT NULL, occurrence_id uuid NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0), from_state text, to_state text NOT NULL,
 cause text NOT NULL, effective_at timestamptz NOT NULL, detected_at timestamptz NOT NULL CHECK(detected_at>=effective_at),
 actor_id uuid REFERENCES public.almanac_users(id), reason text NOT NULL DEFAULT '', evaluation_id uuid,
 request_id text NOT NULL DEFAULT '', CHECK(actor_id IS NULL OR length(btrim(request_id))>0),
 FOREIGN KEY(policy_id,revision,occurrence_id) REFERENCES public.almanac_alert_occurrences(policy_id,revision,id),
 FOREIGN KEY(policy_id,revision,evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id),
 UNIQUE(occurrence_id,sequence),
 CHECK ((from_state IS NULL AND to_state='firing' AND cause='condition_matched') OR
 (from_state='firing' AND to_state='acknowledged' AND cause='acknowledged' AND actor_id IS NOT NULL) OR
 (from_state IN ('firing','acknowledged') AND to_state='resolved' AND cause IN ('conditions_cleared','resolved_after_maintenance_change')) OR
 (from_state IN ('firing','acknowledged') AND to_state='disabled' AND cause IN ('policy_disabled','slo_archived') AND actor_id IS NOT NULL AND length(btrim(reason))>0) OR
 (from_state IN ('firing','acknowledged') AND to_state='superseded' AND cause='definition_superseded' AND actor_id IS NOT NULL AND length(btrim(reason))>0))
);
CREATE TABLE public.almanac_alert_ranges (
 id uuid PRIMARY KEY, policy_id uuid NOT NULL, revision bigint NOT NULL,
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL CHECK (ends_at>starts_at),
 cadence_seconds bigint NOT NULL CHECK(cadence_seconds>0),
 kind text NOT NULL CHECK(kind IN ('repeated_cutoff','unreconstructed_gap','administrative_cancellation')),
 reason text NOT NULL CHECK(length(btrim(reason))>0), recorded_at timestamptz NOT NULL,
 reference_evaluation_id uuid, actor_id uuid REFERENCES public.almanac_users(id),
 request_id text NOT NULL DEFAULT '', CHECK(actor_id IS NULL OR length(btrim(request_id))>0),
 FOREIGN KEY(policy_id,revision) REFERENCES public.almanac_alert_policy_revisions(policy_id,revision),
 FOREIGN KEY(policy_id,revision,reference_evaluation_id) REFERENCES public.almanac_alert_evaluations(policy_id,revision,id),
 UNIQUE(policy_id,revision,starts_at)
);
CREATE FUNCTION public.almanac_immutable_alert_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'alert history is immutable' USING ERRCODE='23514'; END $$;
CREATE TRIGGER almanac_alert_revision_immutable BEFORE UPDATE ON public.almanac_alert_policy_revisions FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_evaluation_immutable BEFORE UPDATE ON public.almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_transition_immutable BEFORE UPDATE ON public.almanac_alert_transitions FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_range_immutable BEFORE UPDATE ON public.almanac_alert_ranges FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_terminal_immutable BEFORE UPDATE ON public.almanac_alert_occurrences FOR EACH ROW
 WHEN (OLD.state IN ('resolved','disabled','superseded')) EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_identity_immutable BEFORE UPDATE OF id,policy_id,revision,firing_evaluation_id,opened_at,policy_snapshot
 ON public.almanac_alert_occurrences FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_alert_history();
CREATE TRIGGER almanac_alert_firing_snapshot_immutable BEFORE UPDATE ON public.almanac_alert_occurrences FOR EACH ROW
 WHEN (OLD.payload->'FiringSnapshot' IS DISTINCT FROM NEW.payload->'FiringSnapshot') EXECUTE FUNCTION public.almanac_immutable_alert_history();
