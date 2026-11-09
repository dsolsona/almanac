-- name: ListSloOverviewPage :many
WITH scope AS (
    SELECT s.id,lower(s.name) AS sort_name,p.payload AS projection_payload,p.urgency AS published_urgency,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL
             AND g.source_generation<=coalesce(source.generation,0)
             AND g.evaluated_through>=sqlc.arg(now)::timestamptz-interval '365 days',false)::boolean AS usable,
           coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
             AND g.calculation_version=sqlc.arg(calculation_version) AND p.slo_id IS NOT NULL
             AND g.source_generation=coalesce(source.generation,0)
             AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
             AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds),false)::boolean AS current,
           jsonb_build_object('slo',jsonb_build_object(
               'id',s.id,'name',s.name,'display_name',s.display_name,'version',s.version::text,'definition_revision',d.revision::text,
               'service_id',d.service_id,'service_display_name',service.display_name,'sli_id',d.sli_id,'sli_display_name',measurement.display_name,
               'target',d.target::text,'rolling_window_seconds',d.rolling_window_seconds,'cadence_seconds',d.cadence_seconds,
               'activated_at',to_char(d.activated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'evaluation_valid_from',to_char(d.evaluation_valid_from AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'historical_requested_by',d.historical_requested_by,
               'historical_requested_at',to_char(d.historical_requested_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'created_at',to_char(s.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               'updated_at',to_char(s.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
               '_preparation_failed',coalesce(f.attempts>=5,false),
               '_source_failed',EXISTS (
                   SELECT 1 FROM public.almanac_derived_dirty_ranges dirty
                   WHERE dirty.sli_id=d.sli_id AND dirty.measurement_revision=d.measurement_revision AND dirty.attempts>=5
                     AND (dirty.lease_until IS NULL OR dirty.lease_until<=sqlc.arg(now)::timestamptz)
                     AND dependency.full_start<dependency.full_end
                     AND dirty.range_start<dependency.full_end AND dirty.range_end>dependency.full_start
               ))::jsonb AS metadata
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
    SELECT *,CASE WHEN current OR (usable AND published_urgency='needs_attention') THEN published_urgency ELSE 'watch' END::text AS urgency,
           CASE WHEN current THEN 'ready'
                WHEN NOT usable THEN 'unavailable'
                WHEN (metadata->>'_preparation_failed')::boolean OR (metadata->>'_source_failed')::boolean THEN 'failed'
                ELSE 'stale' END::text AS current_state
    FROM scope

), ranked AS (
    SELECT *,CASE urgency WHEN 'needs_attention' THEN 0 WHEN 'watch' THEN 1 ELSE 2 END::integer AS rank
    FROM classified
)
SELECT id,sort_name,rank,
       (coalesce(projection_payload,'{}'::jsonb)||metadata||jsonb_build_object('urgency',urgency,'current_state',current_state))::jsonb AS payload,
       usable::boolean AS projection_valid,current::boolean AS projection_current,
       coalesce((metadata->>'_preparation_failed')::boolean,false)::boolean AS preparation_failed,
       coalesce((metadata->>'_source_failed')::boolean,false)::boolean AS source_failed
FROM ranked
WHERE (sqlc.narg(urgency)::text IS NULL OR urgency=sqlc.narg(urgency)::text)
  AND (sqlc.narg(after_id)::uuid IS NULL OR CASE WHEN sqlc.arg(reverse)::boolean
       THEN (rank,sort_name,id)<(sqlc.arg(after_rank)::integer,sqlc.arg(after_sort_name)::text,sqlc.narg(after_id)::uuid)
       ELSE (rank,sort_name,id)>(sqlc.arg(after_rank)::integer,sqlc.arg(after_sort_name)::text,sqlc.narg(after_id)::uuid) END)
ORDER BY CASE WHEN sqlc.arg(reverse)::boolean THEN rank END DESC,
         CASE WHEN sqlc.arg(reverse)::boolean THEN sort_name END DESC,
         CASE WHEN sqlc.arg(reverse)::boolean THEN id END DESC,
         rank,sort_name,id
LIMIT sqlc.arg(page_limit);
