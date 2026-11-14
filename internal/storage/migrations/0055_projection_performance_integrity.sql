-- Validate the three performance objects when the payload is written, not while
-- scanning all SLOs for grouping, scheduling or service summaries. This is a shape
-- check, not a second calculator: unknown/not-applicable values may remain null.
CREATE FUNCTION public.almanac_projection_performance_complete(payload jsonb)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT bool_and(
        coalesce(performance->>'state' IN ('met','violated','unknown','not_applicable'),false)
        AND coalesce(performance->>'budget_unit'='events',false)
        AND NOT EXISTS (
            SELECT 1
            FROM unnest(ARRAY['good','total','bad','attainment','allowed_budget',
                             'remaining_budget','consumed_fraction','remaining_fraction','burn']) field
            WHERE CASE WHEN performance->>'state' IN ('met','violated')
                THEN jsonb_typeof(performance->field) IS DISTINCT FROM 'string'
                ELSE coalesce(jsonb_typeof(performance->field),'null') NOT IN ('null','string')
            END
        )
    )
    FROM (
        SELECT payload->window_name->'performance' AS performance
        FROM unnest(ARRAY['current','burn_1h','burn_6h']) window_name
    ) windows;
$$;

ALTER TABLE public.almanac_slo_current_projections
    ADD COLUMN performance_complete boolean NOT NULL
    GENERATED ALWAYS AS (public.almanac_projection_performance_complete(payload)) STORED;
