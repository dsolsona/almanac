-- Validate newer source hours once per requested scope, then test each tile
-- against that bounded set. Keep the same per-tile generation/overlap fences.
-- Materialize UTF-8 once for both the byte budget and binary wire payload.
-- name: ReadRetainedParts :many
WITH lanes AS (
    SELECT 'attainment'::text AS key,0::integer AS lane_index
    UNION ALL SELECT 'budget',1 UNION ALL SELECT 'burn_1h',2 UNION ALL SELECT 'burn_6h',3
), objective AS (
    SELECT definition.sli_id,definition.cadence_seconds,GREATEST(definition.rolling_window_seconds,21600)::bigint AS window_seconds
    FROM public.almanac_slo_definitions definition
    WHERE definition.slo_id=sqlc.arg(slo_id) AND definition.revision=sqlc.arg(definition_revision)
), requested AS (
    SELECT generate_series(1,cardinality(sqlc.arg(starts)::timestamptz[]))::integer AS ordinal,
           unnest(sqlc.arg(starts)::timestamptz[]) AS range_start,
           unnest(sqlc.arg(ends)::timestamptz[]) AS range_end,
           unnest(sqlc.arg(resolutions)::integer[]) AS resolution
), tiles AS MATERIALIZED (
    SELECT r.ordinal,r.range_start::timestamptz AS range_start,r.range_end::timestamptz AS range_end,
           r.resolution::integer AS resolution,t.slo_id AS present_id,
           sqlc.arg(compliance_only)::boolean AS compliance_only,t.payload AS tile_payload,
           CASE WHEN t.compact_payload->>'payload_digest'=encode(t.payload_digest,'hex')
                  AND t.compact_payload->>'source_generation'=t.source_generation::text
                THEN jsonb_strip_nulls(t.compact_payload) END AS preview,
           coalesce(GREATEST(t.source_generation,
               CASE WHEN checkpoint.coherent_through>=to_timestamp(floor(extract(epoch FROM r.range_end)/o.cadence_seconds)*o.cadence_seconds)
                    THEN checkpoint.coherent_source_generation ELSE 0 END),0)::bigint AS source_generation,
           t.published_at,t.point_count
    FROM requested r LEFT JOIN public.almanac_slo_series_tiles t
      ON t.slo_id=sqlc.arg(slo_id) AND t.definition_revision=sqlc.arg(definition_revision)
     AND t.measurement_revision=sqlc.arg(measurement_revision) AND t.exclusion_revision=0
     AND t.calculation_version=sqlc.arg(calculation_version) AND t.resolution_seconds=r.resolution
     AND t.range_start=r.range_start AND t.range_end=r.range_end
    LEFT JOIN public.almanac_slo_series_repair checkpoint
      ON checkpoint.slo_id=t.slo_id AND checkpoint.definition_revision=t.definition_revision
     AND checkpoint.measurement_revision=t.measurement_revision AND checkpoint.calculation_version=t.calculation_version
    CROSS JOIN objective o
), bounds AS MATERIALIZED (
    SELECT min(range_start) AS range_start,max(range_end) AS range_end,min(source_generation) AS source_generation
    FROM tiles WHERE present_id IS NOT NULL
), newer_dirty AS MATERIALIZED (
    SELECT d.range_start,d.range_end,d.generation
    FROM public.almanac_derived_dirty_ranges d CROSS JOIN objective o CROSS JOIN bounds b
    WHERE d.sli_id=o.sli_id AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND d.generation>b.source_generation
      AND d.range_start>=(date_trunc('hour',(b.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND d.range_start<b.range_end
), newer_aggregates AS MATERIALIZED (
    SELECT a.hour_start,a.source_generation
    FROM public.almanac_sli_hourly_aggregates a CROSS JOIN objective o CROSS JOIN bounds b
    WHERE a.sli_id=o.sli_id AND a.measurement_revision=sqlc.arg(measurement_revision)
      AND a.source_generation>b.source_generation
      AND a.hour_start>=(date_trunc('hour',(b.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND a.hour_start<b.range_end
), selected AS MATERIALIZED (
    SELECT r.ordinal,r.range_start,r.range_end,r.resolution,r.tile_payload,r.preview,r.compliance_only,r.present_id,r.source_generation,r.published_at,r.point_count,
       (EXISTS (
           SELECT 1 FROM public.almanac_slo_series_tile_work w
           WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
             AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.exclusion_revision=0
             AND w.calculation_version=sqlc.arg(calculation_version) AND w.resolution_seconds=r.resolution
             AND w.range_start=r.range_start AND NOT w.summary_only AND w.attempts>=5
       ) OR EXISTS (
           SELECT 1 FROM public.almanac_slo_series_repair w
           WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
             AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.calculation_version=sqlc.arg(calculation_version)
             AND w.cursor_at<r.range_end AND w.end_at>r.range_start AND w.attempts>=5
       ))::boolean AS exhausted,
       (r.present_id IS NOT NULL AND NOT EXISTS (
           SELECT 1 FROM public.almanac_slo_series_tile_work w
           WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
             AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.exclusion_revision=0
             AND w.calculation_version=sqlc.arg(calculation_version)
             AND w.resolution_seconds=r.resolution AND w.range_start=r.range_start AND NOT w.summary_only
       ) AND NOT EXISTS (
           SELECT 1 FROM newer_dirty d
           WHERE d.generation>r.source_generation
             AND d.range_start>=(date_trunc('hour',(r.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
             AND d.range_start<r.range_end AND d.range_end>r.range_start-o.window_seconds*interval '1 second'
       ) AND NOT EXISTS (
           SELECT 1 FROM newer_aggregates a
           WHERE a.source_generation>r.source_generation
             AND a.hour_start>=(date_trunc('hour',(r.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
             AND a.hour_start<r.range_end
       ))::boolean AS ready
    FROM tiles r CROSS JOIN objective o
), lane_counts AS MATERIALIZED (
    -- Count points retained solely for quality transitions; extrema can merge later.
    SELECT lane.key,sum(CASE WHEN s.preview IS NOT NULL THEN (s.preview->'transitions'->>lane.lane_index)::integer
      ELSE (SELECT count(*) FROM jsonb_array_elements(s.tile_payload->lane.key) raw(sample)
            WHERE coalesce(jsonb_array_length(sample->'retained_extrema'),0)=0) END) AS samples
    FROM selected s CROSS JOIN lanes lane
    WHERE s.ready AND NOT s.compliance_only
    GROUP BY lane.key
), coarse_lanes AS MATERIALIZED (
    -- Compose coverage once per day/lane, before selecting its exact extrema.
    SELECT s.ordinal,lane.key,lane.value AS samples,
      jsonb_build_object(
        'states',(SELECT jsonb_agg(DISTINCT value ORDER BY value) FROM jsonb_array_elements(
          jsonb_path_query_array(lane.value,'$[*].state') || jsonb_path_query_array(lane.value,'$[*].display_coverage.states[*]'))),
        'quality_states',(SELECT jsonb_agg(DISTINCT value ORDER BY value) FROM jsonb_array_elements(
          jsonb_path_query_array(lane.value,'$[*].quality.state') || jsonb_path_query_array(lane.value,'$[*].display_coverage.quality_states[*]'))),
        'freshness',(SELECT jsonb_agg(DISTINCT value ORDER BY value) FROM jsonb_array_elements(
          jsonb_path_query_array(lane.value,'$[*].quality.freshness') || jsonb_path_query_array(lane.value,'$[*].display_coverage.freshness[*]')))
      ) AS coverage
    FROM selected s CROSS JOIN LATERAL jsonb_each(s.tile_payload) lane(key,value)
    JOIN lane_counts n ON n.key=lane.key AND n.samples>1500
    WHERE s.ready AND NOT s.compliance_only AND s.preview IS NULL
), coarse_tiles AS MATERIALIZED (
    SELECT c.ordinal,jsonb_object_agg(c.key,(
      SELECT jsonb_agg(sample || jsonb_build_object('display_coverage',c.coverage) ORDER BY sample_ordinal)
      FROM jsonb_array_elements(c.samples) WITH ORDINALITY selected_sample(sample,sample_ordinal)
      WHERE sample->'retained_extrema' ?| ARRAY['first','last','minimum','maximum']
    )) AS payload
    FROM coarse_lanes c GROUP BY c.ordinal
), projected AS MATERIALIZED (
    SELECT s.ordinal,s.range_start,s.range_end,s.resolution,s.source_generation,s.published_at,s.point_count,s.exhausted,s.ready,
      convert_to((CASE WHEN NOT s.ready THEN NULL
        WHEN s.compliance_only AND s.present_id IS NOT NULL THEN coalesce(s.preview->'wire'->>'compliance',
          (coalesce(s.preview->'tile',s.tile_payload)->'compliance')::text,'null')
        WHEN s.preview ? 'wire' THEN (
          SELECT '{' || string_agg(to_json(lane.key)::text || ':' ||
            CASE WHEN n.samples<=1500 AND (s.preview->'transitions'->>lane.lane_index)::integer>0
                 THEN jsonb_strip_nulls(s.tile_payload->lane.key)::text
                 ELSE s.preview->'wire'->>lane.key END,',') ||
            ',"compliance":' || coalesce(s.preview->'wire'->>'compliance','null') || '}'
          FROM lanes lane JOIN lane_counts n ON n.key=lane.key
        )
        ELSE jsonb_strip_nulls(coalesce(s.preview->'tile',s.tile_payload) || coalesce(coarse.payload,'{}'::jsonb) ||
          CASE WHEN s.preview IS NOT NULL THEN (
            SELECT jsonb_object_agg(lane.key,
              CASE WHEN n.samples<=1500 AND (s.preview->'transitions'->>lane.lane_index)::integer>0
                   THEN s.tile_payload->lane.key ELSE coalesce(s.preview->'tile',s.tile_payload)->lane.key END)
            FROM lanes lane
            JOIN lane_counts n ON n.key=lane.key
          ) ELSE '{}'::jsonb END)::text
        END),'UTF8') AS payload,
      CASE WHEN s.ready AND s.resolution=86400 AND NOT s.compliance_only THEN
        convert_to(coalesce(s.preview->'wire'->>'compliance',
          (coalesce(s.preview->'tile',s.tile_payload)->'compliance')::text,'null'),'UTF8') END AS compliance_payload
    FROM selected s LEFT JOIN coarse_tiles coarse ON coarse.ordinal=s.ordinal
), bounded AS (
SELECT *,coalesce(sum(octet_length(payload)+coalesce(octet_length(compliance_payload),0)) OVER (),0)::bigint AS payload_bytes FROM projected
)
SELECT ordinal,range_start,range_end,resolution,
       CASE WHEN payload_bytes<=20971520 THEN coalesce(payload,'{}'::bytea) ELSE '{}'::bytea END::bytea AS payload,
       CASE WHEN payload_bytes<=20971520 THEN coalesce(compliance_payload,''::bytea) ELSE ''::bytea END::bytea AS compliance_payload,
       payload_bytes,source_generation,published_at,point_count,exhausted,ready
FROM bounded ORDER BY ordinal;

-- name: ReadRetainedEdgePoints :many
WITH objective AS (
    SELECT definition.sli_id,GREATEST(definition.rolling_window_seconds,21600)::bigint AS window_seconds
    FROM public.almanac_slo_definitions definition WHERE definition.slo_id=sqlc.arg(slo_id) AND definition.revision=sqlc.arg(definition_revision)
), points AS MATERIALIZED (
    SELECT p.evaluation_at,convert_to(p.payload::text,'UTF8') AS payload,
           GREATEST(p.source_generation,
               CASE WHEN checkpoint.coherent_through>=p.evaluation_at THEN checkpoint.coherent_source_generation ELSE 0 END)::bigint AS source_generation
    FROM public.almanac_slo_series_points p
    LEFT JOIN public.almanac_slo_series_repair checkpoint
      ON checkpoint.slo_id=p.slo_id AND checkpoint.definition_revision=p.definition_revision
     AND checkpoint.measurement_revision=p.measurement_revision AND checkpoint.calculation_version=p.calculation_version
    WHERE p.slo_id=sqlc.arg(slo_id) AND p.definition_revision=sqlc.arg(definition_revision) AND p.measurement_revision=sqlc.arg(measurement_revision)
      AND p.exclusion_revision=0 AND p.calculation_version=sqlc.arg(calculation_version) AND p.evaluation_at>sqlc.arg(evaluation_at) AND p.evaluation_at<=sqlc.arg(evaluation_at_2)
    ORDER BY p.evaluation_at LIMIT 961
), bounds AS MATERIALIZED (
    SELECT min(evaluation_at) AS range_start,max(evaluation_at) AS range_end,min(source_generation) AS source_generation
    FROM points
), newer_dirty AS MATERIALIZED (
    SELECT d.range_start,d.range_end,d.generation
    FROM public.almanac_derived_dirty_ranges d CROSS JOIN objective o CROSS JOIN bounds b
    WHERE d.sli_id=o.sli_id AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND d.generation>b.source_generation
      AND d.range_start>=(date_trunc('hour',(b.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND d.range_start<b.range_end
), newer_aggregates AS MATERIALIZED (
    SELECT a.hour_start,a.source_generation
    FROM public.almanac_sli_hourly_aggregates a CROSS JOIN objective o CROSS JOIN bounds b
    WHERE a.sli_id=o.sli_id AND a.measurement_revision=sqlc.arg(measurement_revision)
      AND a.source_generation>b.source_generation
      AND a.hour_start>=(date_trunc('hour',(b.range_start-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
      AND a.hour_start<b.range_end
), selected AS (
    SELECT p.evaluation_at,p.payload,
           sum(octet_length(p.payload)) OVER ()::bigint AS payload_bytes,
           (EXISTS (
               SELECT 1 FROM newer_dirty d
               WHERE d.generation>p.source_generation
                 AND d.range_start>=(date_trunc('hour',(p.evaluation_at-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
                 AND d.range_start<p.evaluation_at AND d.range_end>p.evaluation_at-o.window_seconds*interval '1 second'
           ) OR EXISTS (
               SELECT 1 FROM newer_aggregates a
               WHERE a.source_generation>p.source_generation
                 AND a.hour_start>=(date_trunc('hour',(p.evaluation_at-o.window_seconds*interval '1 second') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
                 AND a.hour_start<p.evaluation_at
           ))::boolean AS repairing,
           EXISTS (
               SELECT 1 FROM public.almanac_slo_series_repair w
               WHERE w.slo_id=sqlc.arg(slo_id) AND w.definition_revision=sqlc.arg(definition_revision)
                 AND w.measurement_revision=sqlc.arg(measurement_revision) AND w.calculation_version=sqlc.arg(calculation_version)
                 AND w.cursor_at<p.evaluation_at AND w.end_at>=p.evaluation_at AND w.attempts>=5
           ) AS exhausted
    FROM points p CROSS JOIN objective o
)
SELECT evaluation_at,CASE WHEN payload_bytes<=20971520 THEN payload ELSE '{}'::bytea END::bytea AS payload,
       payload_bytes,repairing,exhausted FROM selected ORDER BY evaluation_at;
