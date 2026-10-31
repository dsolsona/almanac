-- Reuse unchanged ready pages without extending their original expiry or
-- sharing their actor/session-bound cursors with another reader identity.
ALTER TABLE public.almanac_slo_overview_snapshots ADD COLUMN publication_fingerprint bytea;

CREATE TABLE public.almanac_sli_source_generations (
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (sli_id, measurement_revision),
    FOREIGN KEY (sli_id, measurement_revision)
        REFERENCES public.almanac_sli_measurements(sli_id, revision)
);

CREATE TABLE public.almanac_derived_dirty_ranges (
    id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    next_attempt_at timestamptz NOT NULL,
    last_error text,
    manual_retried_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (sli_id, measurement_revision, range_start),
    FOREIGN KEY (sli_id, measurement_revision)
        REFERENCES public.almanac_sli_source_generations(sli_id, measurement_revision),
    CHECK (range_end > range_start)
);
CREATE INDEX almanac_derived_dirty_due_idx
    ON public.almanac_derived_dirty_ranges(next_attempt_at, sli_id, measurement_revision)
    WHERE attempts < 5;

-- The trigger participates in the canonical acceptance transaction. Conflicting
-- or duplicate inserts do not fire it; rollback removes both data and dirty work.
CREATE FUNCTION public.almanac_mark_derived_dirty() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE next_generation bigint;
BEGIN
    INSERT INTO public.almanac_sli_source_generations
        (sli_id, measurement_revision, generation, updated_at)
    VALUES (NEW.sli_id, NEW.measurement_revision, 1, NEW.recorded_at)
    ON CONFLICT (sli_id, measurement_revision) DO UPDATE
        SET generation=almanac_sli_source_generations.generation+1,
            updated_at=GREATEST(almanac_sli_source_generations.updated_at, EXCLUDED.updated_at)
    RETURNING generation INTO next_generation;

    INSERT INTO public.almanac_derived_dirty_ranges
        (sli_id, measurement_revision, generation, range_start, range_end, next_attempt_at, updated_at)
    VALUES (NEW.sli_id, NEW.measurement_revision, next_generation,
            (date_trunc('hour', NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
            GREATEST((date_trunc('hour', NEW.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour', NEW.interval_end),
            NEW.recorded_at, NEW.recorded_at)
    ON CONFLICT (sli_id, measurement_revision, range_start) DO UPDATE
        SET generation=EXCLUDED.generation,
            range_start=LEAST(almanac_derived_dirty_ranges.range_start, EXCLUDED.range_start),
            range_end=GREATEST(almanac_derived_dirty_ranges.range_end, EXCLUDED.range_end),
            next_attempt_at=LEAST(almanac_derived_dirty_ranges.next_attempt_at, EXCLUDED.next_attempt_at),
            updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at, EXCLUDED.updated_at);
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_sli_derived_dirty
    AFTER INSERT ON public.almanac_sli_coverage
    FOR EACH ROW EXECUTE FUNCTION public.almanac_mark_derived_dirty();

-- Prepare existing retained observations once when upgrading. This does not
-- reinterpret their measurements or change their receipt/authorization evidence.
INSERT INTO public.almanac_sli_source_generations
    (sli_id, measurement_revision, generation, updated_at)
SELECT sli_id, measurement_revision, 1, max(recorded_at)
FROM public.almanac_sli_coverage GROUP BY sli_id, measurement_revision;
INSERT INTO public.almanac_derived_dirty_ranges
    (sli_id, measurement_revision, generation, range_start, range_end, next_attempt_at, updated_at)
SELECT sli_id, measurement_revision, 1,
       date_trunc('hour', interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
       GREATEST((date_trunc('hour', interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour', max(interval_end)),
       CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM public.almanac_sli_coverage GROUP BY sli_id, measurement_revision,
    date_trunc('hour', interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';

CREATE TABLE public.almanac_sli_hourly_aggregates (
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    hour_start timestamptz NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation > 0),
    good numeric NOT NULL CHECK (good >= 0),
    total numeric NOT NULL CHECK (total >= good),
    observed_intervals bigint NOT NULL CHECK (observed_intervals >= 0),
    known_empty_intervals bigint NOT NULL CHECK (known_empty_intervals >= 0),
    zero_traffic_intervals bigint NOT NULL CHECK (zero_traffic_intervals >= 0),
    first_interval_start timestamptz NOT NULL,
    last_interval_end timestamptz NOT NULL,
    latest_receipt_at timestamptz NOT NULL,
    first_observation_at timestamptz,
    last_observation_at timestamptz,
    PRIMARY KEY (sli_id, measurement_revision, hour_start),
    FOREIGN KEY (sli_id, measurement_revision)
        REFERENCES public.almanac_sli_measurements(sli_id, revision),
    CHECK (hour_start = date_trunc('hour', hour_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
);

CREATE TABLE public.almanac_slo_derived_generations (
    id uuid PRIMARY KEY,
    slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation >= 0),
    exclusion_revision bigint NOT NULL DEFAULT 0 CHECK (exclusion_revision >= 0),
    calculation_version text NOT NULL,
    evaluated_through timestamptz NOT NULL,
    retention_start timestamptz NOT NULL,
    published_at timestamptz NOT NULL,
    FOREIGN KEY (slo_id, definition_revision)
        REFERENCES public.almanac_slo_definitions(slo_id, revision)
);
CREATE INDEX almanac_slo_derived_objective_idx
    ON public.almanac_slo_derived_generations(slo_id, evaluated_through DESC);

CREATE TABLE public.almanac_slo_current_projections (
    slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id),
    generation_id uuid NOT NULL REFERENCES public.almanac_slo_derived_generations(id),
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    sort_name text NOT NULL,
    urgency text NOT NULL CHECK (urgency IN ('needs_attention', 'watch', 'healthy')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    payload_digest bytea NOT NULL
);
CREATE INDEX almanac_slo_current_groups_idx
    ON public.almanac_slo_current_projections(urgency, sort_name, slo_id);
CREATE INDEX almanac_slo_current_service_idx
    ON public.almanac_slo_current_projections(service_id, urgency, sort_name, slo_id);

-- A change of source generation does not reset an exhausted automatic retry
-- budget. A new calculation/definition revision can be attempted independently.
CREATE TABLE public.almanac_slo_current_failures (
    slo_id uuid PRIMARY KEY REFERENCES public.almanac_slos(id),
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    calculation_version text NOT NULL,
    attempts integer NOT NULL CHECK (attempts BETWEEN 0 AND 5),
    next_attempt_at timestamptz NOT NULL,
    last_error text NOT NULL,
    manual_retried_at timestamptz,
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision)
);

CREATE TABLE public.almanac_slo_history_blocks (
    generation_id uuid NOT NULL REFERENCES public.almanac_slo_derived_generations(id) ON DELETE CASCADE,
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    burn_window_seconds integer NOT NULL CHECK (burn_window_seconds IN (3600, 21600)),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    payload_digest bytea NOT NULL,
    kind text NOT NULL DEFAULT 'detail' CHECK (kind IN ('detail','overview')),
    budget_sparkline jsonb CHECK (jsonb_typeof(budget_sparkline)='array' AND jsonb_array_length(budget_sparkline)<=120),
    PRIMARY KEY (generation_id, range_start, range_end, burn_window_seconds),
    CHECK (range_end > range_start)
);

CREATE TABLE public.almanac_slo_history_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slo_id uuid NOT NULL REFERENCES public.almanac_slos(id),
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation >= 0),
    calculation_version text NOT NULL,
    retention_start timestamptz NOT NULL,
    evaluated_through timestamptz NOT NULL,
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    burn_window_seconds integer NOT NULL CHECK (burn_window_seconds IN (3600,21600)),
    kind text NOT NULL DEFAULT 'detail' CHECK (kind IN ('detail','overview')),
    state text NOT NULL CHECK (state IN ('pending','running','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    lease_until timestamptz,
    next_attempt_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    last_error text,
    manual_retried_at timestamptz,
    UNIQUE (slo_id,definition_revision,measurement_revision,source_generation,evaluated_through,range_start,range_end,burn_window_seconds,kind,calculation_version,retention_start),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (range_end > range_start)
);
CREATE INDEX almanac_slo_history_jobs_due_idx
    ON public.almanac_slo_history_jobs(next_attempt_at,created_at) WHERE state <> 'failed';
CREATE INDEX almanac_slo_history_blocks_lookup_idx
    ON public.almanac_slo_history_blocks(range_start,range_end,burn_window_seconds,generation_id);

-- Reusable exact display blocks. Complete response generations above remain
-- immutable even when an affected block is replaced after a late observation.
CREATE TABLE public.almanac_slo_display_blocks (
    slo_id uuid NOT NULL,
    definition_revision bigint NOT NULL,
    measurement_revision bigint NOT NULL,
    calculation_version text NOT NULL,
    lane text NOT NULL CHECK (lane IN ('budget','burn','attainment','compliance')),
    window_seconds bigint NOT NULL CHECK (window_seconds>=0),
    freshness_grace_us bigint NOT NULL CHECK (freshness_grace_us>=0),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    include_start boolean NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation>=0),
    retention_start timestamptz NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    payload_digest bytea NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (slo_id,definition_revision,measurement_revision,calculation_version,
                 lane,window_seconds,freshness_grace_us,range_start,range_end,include_start),
    FOREIGN KEY (slo_id,definition_revision) REFERENCES public.almanac_slo_definitions(slo_id,revision),
    CHECK (range_end>range_start)
);
CREATE INDEX almanac_slo_display_blocks_expiry_idx ON public.almanac_slo_display_blocks(updated_at);

-- The bounded derived cache cannot consume canonical storage without limit.
-- This counter excludes current projections, so a history transaction cannot
-- block the current worker on a shared budget row. Only history writes use it.
CREATE TABLE public.almanac_derived_cache_usage (
    singleton boolean PRIMARY KEY CHECK (singleton),
    payload_bytes bigint NOT NULL CHECK (payload_bytes BETWEEN 0 AND 2147483648)
);
INSERT INTO public.almanac_derived_cache_usage VALUES (true,0);
CREATE FUNCTION public.almanac_account_derived_cache() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE change_bytes bigint := 0;
BEGIN
    IF TG_OP<>'DELETE' THEN change_bytes:=octet_length(NEW.payload::text); END IF;
    IF TG_OP<>'INSERT' THEN change_bytes:=change_bytes-octet_length(OLD.payload::text); END IF;
    IF change_bytes<>0 THEN
        UPDATE public.almanac_derived_cache_usage SET payload_bytes=payload_bytes+change_bytes
        WHERE singleton AND payload_bytes+change_bytes BETWEEN 0 AND 2147483648;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='54000',MESSAGE='derived cache budget exhausted';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_history_cache_usage AFTER INSERT OR UPDATE OF payload OR DELETE
    ON public.almanac_slo_history_blocks FOR EACH ROW EXECUTE FUNCTION public.almanac_account_derived_cache();
CREATE TRIGGER almanac_display_cache_usage AFTER INSERT OR UPDATE OF payload OR DELETE
    ON public.almanac_slo_display_blocks FOR EACH ROW EXECUTE FUNCTION public.almanac_account_derived_cache();
