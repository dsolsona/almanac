ALTER TABLE public.almanac_datasources
    ADD COLUMN assignment_configuration_revision bigint NOT NULL DEFAULT 0 CHECK (assignment_configuration_revision >= 0);
ALTER TABLE public.almanac_datasource_reassignments
    ADD COLUMN target_configuration_revision bigint NOT NULL DEFAULT 0 CHECK (target_configuration_revision >= 0);
ALTER TABLE public.almanac_datasource_reassignments
    DROP CONSTRAINT almanac_datasource_reassignments_state_check;
ALTER TABLE public.almanac_datasource_reassignments
    ADD CONSTRAINT almanac_datasource_reassignments_state_check
    CHECK (state IN ('waiting_for_previous_agent', 'replacement_unavailable', 'waiting_for_configuration', 'waiting_for_collection'));
