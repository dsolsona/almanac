-- Grouping, scheduling and freshness checks need one scalar, not the chart payload.
-- Keep its wire text generated from JSON so removal/corruption/republication cannot
-- leave a separately maintained timestamp claiming that an old payload is current.
ALTER TABLE public.almanac_slo_current_projections
    ADD COLUMN current_valid_until text
    GENERATED ALWAYS AS (payload->>'_current_valid_until') STORED;

-- Invalid cache metadata expires instead of aborting an entire list or due scan.
-- The cast remains STABLE (DateStyle/timezone), rather than falsely declaring an
-- immutable timestamp parser just to put that cast in a generated column.
CREATE FUNCTION public.almanac_projection_deadline(value text)
RETURNS timestamptz
LANGUAGE sql STABLE STRICT PARALLEL SAFE
AS $$
    SELECT CASE WHEN pg_input_is_valid(value, 'timestamp with time zone')
        THEN CASE WHEN isfinite(value::timestamptz) THEN value::timestamptz END
    END;
$$;
