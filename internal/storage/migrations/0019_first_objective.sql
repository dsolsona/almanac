CREATE TABLE public.almanac_services (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    display_name text NOT NULL CHECK (octet_length(display_name) BETWEEN 1 AND 512),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE public.almanac_slos (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    display_name text NOT NULL CHECK (octet_length(display_name) BETWEEN 1 AND 512),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    definition_revision bigint NOT NULL DEFAULT 1 CHECK (definition_revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE public.almanac_slo_definitions (
    slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
    revision bigint NOT NULL CHECK (revision > 0),
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    target numeric(38,12) NOT NULL CHECK (target >= 0 AND target < 1),
    rolling_window_seconds bigint NOT NULL CHECK (rolling_window_seconds BETWEEN 1 AND 2147483647),
    cadence_seconds integer NOT NULL CHECK (cadence_seconds BETWEEN 15 AND 86400),
    activated_at timestamptz NOT NULL,
    evaluation_valid_from timestamptz NOT NULL,
    historical_requested_by uuid REFERENCES public.almanac_users(id),
    historical_requested_at timestamptz,
    PRIMARY KEY (slo_id, revision),
    FOREIGN KEY (sli_id, measurement_revision) REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (rolling_window_seconds % cadence_seconds = 0),
    CHECK (evaluation_valid_from <= activated_at),
    CHECK ((historical_requested_by IS NULL AND historical_requested_at IS NULL AND evaluation_valid_from = activated_at) OR
           (historical_requested_by IS NOT NULL AND historical_requested_at IS NOT NULL))
);

CREATE INDEX almanac_services_page_idx ON public.almanac_services(created_at, id);
CREATE INDEX almanac_slos_page_idx ON public.almanac_slos(created_at, id);
CREATE INDEX almanac_slo_definitions_service_idx ON public.almanac_slo_definitions(service_id, slo_id, revision);

ALTER TABLE public.almanac_slos ADD CONSTRAINT almanac_slos_current_definition_fk
    FOREIGN KEY (id, definition_revision) REFERENCES public.almanac_slo_definitions(slo_id, revision)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE public.almanac_objective_create_receipts (
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id),
    operation text NOT NULL CHECK (operation IN ('service.create', 'slo.create')),
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    result_id uuid NOT NULL,
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (actor_id, operation, idempotency_key),
    CHECK (expires_at > created_at)
);
