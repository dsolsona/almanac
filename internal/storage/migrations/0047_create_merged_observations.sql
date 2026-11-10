CREATE TABLE public.almanac_sli_observations (
    sli_id uuid NOT NULL,
    measurement_revision bigint NOT NULL,
    interval_start timestamptz NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('observed','complete_empty','incomplete','failed')),
    good_count numeric(38,12),
    total_count numeric(38,12),
    result_digest bytea NOT NULL CHECK (octet_length(result_digest)=32),
    error_code text CHECK (error_code IS NULL OR error_code IN (
        'authentication','incomplete','network','query','rate_limited','tls','timeout',
        'unsupported','assignment_revoked','deadline')),
    missing_components text[] NOT NULL DEFAULT '{}',
    execution_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    agent_id uuid NOT NULL REFERENCES public.almanac_agents(id),
    session_generation bigint NOT NULL CHECK (session_generation>0),
    configuration_revision bigint NOT NULL CHECK (configuration_revision>0),
    lease_id text NOT NULL REFERENCES public.almanac_agent_execution_grants(lease_id),
    agent_created_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    ingestion_generation bigint NOT NULL DEFAULT 0 CHECK (ingestion_generation>=0),
    PRIMARY KEY (sli_id,measurement_revision,interval_start),
    FOREIGN KEY (sli_id,measurement_revision) REFERENCES public.almanac_sli_measurements(sli_id,revision),
    CHECK ((outcome='observed' AND good_count IS NOT NULL AND total_count IS NOT NULL
            AND good_count>=0 AND total_count>=good_count)
        OR (outcome<>'observed' AND good_count IS NULL AND total_count IS NULL)),
    CHECK (octet_length(lease_id) BETWEEN 32 AND 64),
    CHECK (missing_components <@ ARRAY['good','total']::text[]),
    CHECK ((outcome='failed' AND error_code IS NOT NULL AND cardinality(missing_components)=0)
        OR (outcome='incomplete' AND error_code IS NULL AND cardinality(missing_components) BETWEEN 1 AND 2)
        OR (outcome IN ('observed','complete_empty') AND error_code IS NULL AND cardinality(missing_components)=0))
) PARTITION BY RANGE (interval_start);

CREATE INDEX almanac_sli_observations_observed_idx
ON public.almanac_sli_observations(sli_id,measurement_revision,interval_start DESC) WHERE outcome='observed';
CREATE INDEX almanac_sli_observations_history_idx
ON public.almanac_sli_observations(sli_id,interval_start DESC,recorded_at DESC);
-- Retained leases and late receipts have consumers independent of interval order.
CREATE INDEX almanac_sli_observations_lease_idx ON public.almanac_sli_observations(lease_id);
CREATE INDEX almanac_sli_observations_receipt_idx
ON public.almanac_sli_observations(sli_id,recorded_at DESC,interval_start DESC);

DO $$
DECLARE part record; bounds text[]; start_at timestamptz; end_at timestamptz; suffix text;
BEGIN
    FOR part IN
        SELECT child.relname,pg_get_expr(child.relpartbound,child.oid) AS bound
        FROM pg_inherits i JOIN pg_class child ON child.oid=i.inhrelid
        JOIN pg_namespace ns ON ns.oid=child.relnamespace
        WHERE i.inhparent='public.almanac_sli_coverage'::regclass AND ns.nspname='public'
        ORDER BY child.relname
    LOOP
        bounds:=regexp_match(part.bound,'^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$');
        IF bounds IS NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='unexpected legacy observation partition';
        END IF;
        start_at:=bounds[1]::timestamptz; end_at:=bounds[2]::timestamptz;
        suffix:=to_char(start_at AT TIME ZONE 'UTC','YYYYMM');
        IF start_at<>(date_trunc('month',start_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
            OR end_at<>((start_at AT TIME ZONE 'UTC')+interval '1 month') AT TIME ZONE 'UTC'
            OR part.relname<>'almanac_sli_coverage_'||suffix THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='unexpected legacy observation partition';
        END IF;
        EXECUTE format('CREATE TABLE public.%I PARTITION OF public.almanac_sli_observations %s',
            'almanac_sli_observations_'||suffix,part.bound);
    END LOOP;
END;
$$;

-- The pending schema gate stops application admission; these fences also close
-- already admitted old writers while the resumable copy commits its batches.
ALTER TABLE public.almanac_sli_coverage ADD CONSTRAINT almanac_observations_copy_fence CHECK(false) NOT VALID;
ALTER TABLE public.almanac_sli_observation_values ADD CONSTRAINT almanac_observations_copy_fence CHECK(false) NOT VALID;
