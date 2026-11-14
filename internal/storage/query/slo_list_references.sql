-- name: ListSloOverviewPage :many
-- Reuse classification scalars without expanding them through every presentation
-- expression. Fetch chart payloads and construct metadata only for the chosen page.
WITH scalar_scope AS MATERIALIZED (
    SELECT s.id,lower(s.name) AS sort_name,p.urgency AS published_urgency,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL AND p.performance_complete
             AND g.source_generation<=coalesce(source.generation,0)
             AND g.evaluated_through>=sqlc.arg(now)::timestamptz-interval '365 days',false)::boolean AS usable,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL AND p.performance_complete
             AND g.source_generation=coalesce(source.generation,0)
             AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
             AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND coalesce(public.almanac_projection_deadline(p.current_valid_until),'-infinity'::timestamptz)>sqlc.arg(now)::timestamptz,false)::boolean AS current,
           s.name,s.display_name,s.version::text AS version,d.revision::text AS definition_revision,
           d.service_id,service.display_name AS service_display_name,d.sli_id,measurement.display_name AS sli_display_name,
           d.target::text AS target,d.rolling_window_seconds,d.cadence_seconds,d.activated_at,d.evaluation_valid_from,
           d.historical_requested_by,d.historical_requested_at,s.created_at,s.updated_at,
           d.measurement_revision,dependency.full_start,dependency.full_end,
           coalesce(f.attempts>=5,false) AS preparation_failed
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    JOIN public.almanac_services service ON service.id=d.service_id
    JOIN public.almanac_slis measurement ON measurement.id=d.sli_id
    JOIN public.almanac_sli_measurements m ON m.sli_id=d.sli_id AND m.revision=d.measurement_revision
    CROSS JOIN LATERAL (
        SELECT 3600::bigint/gcd(3600::bigint,d.cadence_seconds::bigint)*d.cadence_seconds AS grid,
               to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds) AS at
    ) boundary
    CROSS JOIN LATERAL (
        SELECT to_timestamp(ceil(extract(epoch FROM greatest(
                   boundary.at-greatest(d.rolling_window_seconds,CASE WHEN 21600%d.cadence_seconds=0 THEN 21600 ELSE 0 END)*interval '1 second',
                   d.evaluation_valid_from,m.effective_from,sqlc.arg(now)::timestamptz-interval '365 days'))/boundary.grid)*boundary.grid) AS full_start,
               to_timestamp(floor(extract(epoch FROM boundary.at)/boundary.grid)*boundary.grid) AS full_end
    ) dependency
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
    LEFT JOIN public.almanac_slo_current_failures f ON f.slo_id=s.id AND f.definition_revision=d.revision
      AND f.measurement_revision=d.measurement_revision AND f.calculation_version=sqlc.arg(calculation_version)
    WHERE (sqlc.narg(query_text)::text IS NULL OR strpos(lower(s.name||' '||s.display_name),lower(sqlc.narg(query_text)::text))>0)
      AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id=sqlc.narg(service_id)::uuid)
), classified AS (
    SELECT *,CASE WHEN current OR (usable AND published_urgency='needs_attention') THEN published_urgency ELSE 'watch' END::text AS urgency
    FROM scalar_scope
), ranked AS (
    SELECT *,CASE urgency WHEN 'needs_attention' THEN 0 WHEN 'watch' THEN 1 ELSE 2 END::integer AS rank
    FROM classified
), selected AS MATERIALIZED (
    SELECT * FROM ranked
WHERE (sqlc.narg(urgency)::text IS NULL OR urgency=sqlc.narg(urgency)::text)
  AND (sqlc.narg(after_id)::uuid IS NULL OR CASE WHEN sqlc.arg(reverse)::boolean
       THEN (rank,sort_name,id)<(sqlc.arg(after_rank)::integer,sqlc.arg(after_sort_name)::text,sqlc.narg(after_id)::uuid)
       ELSE (rank,sort_name,id)>(sqlc.arg(after_rank)::integer,sqlc.arg(after_sort_name)::text,sqlc.narg(after_id)::uuid) END)
ORDER BY CASE WHEN sqlc.arg(reverse)::boolean THEN rank END DESC,
         CASE WHEN sqlc.arg(reverse)::boolean THEN sort_name END DESC,
         CASE WHEN sqlc.arg(reverse)::boolean THEN id END DESC,
         rank,sort_name,id
LIMIT sqlc.arg(page_limit)
), page_metadata AS MATERIALIZED (
    SELECT page.*,CASE WHEN p.performance_complete THEN p.payload END AS projection_payload,
           jsonb_build_object('slo',jsonb_build_object(
               'id',page.id,'name',page.name,'display_name',page.display_name,'version',page.version,'definition_revision',page.definition_revision,
               'service_id',page.service_id,'service_display_name',page.service_display_name,'sli_id',page.sli_id,'sli_display_name',page.sli_display_name,
               'target',page.target,'rolling_window_seconds',page.rolling_window_seconds,'cadence_seconds',page.cadence_seconds,
               'activated_at',to_char(page.activated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'evaluation_valid_from',to_char(page.evaluation_valid_from AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'historical_requested_by',page.historical_requested_by,
               'historical_requested_at',to_char(page.historical_requested_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'created_at',to_char(page.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'updated_at',to_char(page.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
               '_preparation_failed',page.preparation_failed,
               '_source_failed',EXISTS (
                   SELECT 1 FROM public.almanac_derived_dirty_ranges dirty
                   WHERE dirty.sli_id=page.sli_id AND dirty.measurement_revision=page.measurement_revision AND dirty.attempts>=5
                     AND (dirty.lease_until IS NULL OR dirty.lease_until<=sqlc.arg(now)::timestamptz)
                     AND page.full_start<page.full_end
                     AND dirty.range_start<page.full_end AND dirty.range_end>page.full_start
               ))::jsonb AS metadata
    FROM selected page
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=page.id
), presented AS (
    SELECT *,CASE WHEN sqlc.arg(reverse)::boolean THEN rank END AS reverse_rank,
           CASE WHEN sqlc.arg(reverse)::boolean THEN sort_name END AS reverse_name,
           CASE WHEN sqlc.arg(reverse)::boolean THEN id END AS reverse_id,
           CASE WHEN current THEN 'ready'
                WHEN NOT usable THEN 'unavailable'
                WHEN preparation_failed OR (metadata->>'_source_failed')::boolean THEN 'failed'
                ELSE 'stale' END::text AS current_state
    FROM page_metadata
)
SELECT id,sort_name,rank,
       (coalesce(projection_payload,'{}'::jsonb)||metadata||jsonb_build_object('urgency',urgency,'current_state',current_state))::jsonb AS payload,
       usable::boolean AS projection_valid,current::boolean AS projection_current,
       preparation_failed::boolean AS preparation_failed,
       coalesce((metadata->>'_source_failed')::boolean,false)::boolean AS source_failed
-- Keep assembly above the scalar/reference sort, including maximum API pages.
-- Expose order keys through the subquery so the outer order needs no payload sort.
FROM (
    SELECT * FROM presented
    ORDER BY reverse_rank DESC,reverse_name DESC,reverse_id DESC,rank,sort_name,id
    OFFSET 0
) ordered
ORDER BY reverse_rank DESC,reverse_name DESC,reverse_id DESC,rank,sort_name,id;
