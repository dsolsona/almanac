CREATE TABLE public.almanac_datasource_reassignments (
    datasource_id uuid PRIMARY KEY REFERENCES public.almanac_datasources(id),
    source_agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    target_agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    requested_at timestamptz NOT NULL,
    authority_expires_at timestamptz NOT NULL,
    removal_revision bigint NOT NULL CHECK (removal_revision > 0),
    source_config_version bigint NOT NULL CHECK (source_config_version > 0),
    uses_v2_removal boolean NOT NULL,
    drained_at timestamptz,
    retry_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('waiting_for_previous_agent', 'replacement_unavailable')),
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
    request_id text NOT NULL,
    CHECK (source_agent_id <> target_agent_id)
);
CREATE INDEX almanac_datasource_reassignment_target_idx ON public.almanac_datasource_reassignments(target_agent_id);

CREATE TABLE public.almanac_sli_ownership_epochs (
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz,
    configuration_revision bigint NOT NULL DEFAULT 0 CHECK (configuration_revision >= 0),
    PRIMARY KEY (sli_id, measurement_revision, starts_at),
    FOREIGN KEY (sli_id, measurement_revision) REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE UNIQUE INDEX almanac_sli_ownership_current_idx ON public.almanac_sli_ownership_epochs(sli_id, measurement_revision) WHERE ends_at IS NULL;

CREATE TABLE public.almanac_agent_handoff_configurations (
    agent_id uuid PRIMARY KEY REFERENCES public.almanac_agents(id),
    revision bigint NOT NULL CHECK (revision > 0)
);
