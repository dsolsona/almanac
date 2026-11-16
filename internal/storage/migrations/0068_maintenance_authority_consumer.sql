-- The authority queue also retains its bounded scan checkpoint. It is not a
-- second history store; retained buckets continue through shared preparation.
ALTER TABLE public.almanac_slo_exclusion_work
 ADD COLUMN processed_revision bigint NOT NULL DEFAULT 0 CHECK(processed_revision>=0),
 ADD COLUMN progress_part smallint NOT NULL DEFAULT 0 CHECK(progress_part BETWEEN 0 AND 2),
 ADD COLUMN progress_hour timestamptz,
 ADD COLUMN progress_until timestamptz,
 ADD COLUMN scheduled_through timestamptz,
 ADD COLUMN current_revision bigint NOT NULL DEFAULT 0 CHECK(current_revision>=0),
 ADD COLUMN boundary_through timestamptz,
 ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 ADD COLUMN failure_version bigint NOT NULL DEFAULT 0,
 ADD COLUMN failure_reason text NOT NULL DEFAULT '',
 ADD COLUMN lease_token uuid,
 ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0),
 ADD COLUMN lease_until timestamptz,
 ADD COLUMN claimed_version bigint,
 ADD COLUMN claimed_revision bigint,
 ADD COLUMN last_tick timestamptz,
 ADD CONSTRAINT almanac_authority_progress_revision CHECK(processed_revision<=desired_revision),
 ADD CONSTRAINT almanac_authority_lease CHECK((lease_token IS NULL)=(lease_until IS NULL));
CREATE INDEX almanac_authority_due_idx ON public.almanac_slo_exclusion_work(next_attempt_at,slo_id);
-- Immutable disjoint inputs already have scoped GiST range lookup. Ordered
-- boundary seeks additionally avoid scanning a retained revision chain.
CREATE INDEX almanac_maintenance_input_start_idx ON public.almanac_maintenance_changed_inputs(slo_id,lower(input_range));
CREATE INDEX almanac_maintenance_input_end_idx ON public.almanac_maintenance_changed_inputs(slo_id,upper(input_range));
-- A superseded unfinished target is transferred once, without rewriting its
-- immutable identity or reviving its lease/failure budget.
ALTER TABLE public.almanac_maintenance_preparation ADD COLUMN authority_retargeted_revision bigint NOT NULL DEFAULT 0 CHECK(authority_retargeted_revision>=0);
CREATE INDEX almanac_maintenance_retarget_idx ON public.almanac_maintenance_preparation(slo_id,id)
 WHERE kind IN ('extrema_hour','extrema_day') AND state<>'complete' AND authority_retargeted_revision=0;
