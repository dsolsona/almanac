-- name: ExtendRetainedSeriesRepair :exec
INSERT INTO public.almanac_slo_series_repair
    (slo_id,definition_revision,measurement_revision,calculation_version,cursor_at,end_at,next_attempt_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (slo_id,definition_revision,measurement_revision,calculation_version)
DO UPDATE SET end_at=EXCLUDED.end_at,version=almanac_slo_series_repair.version+1
WHERE almanac_slo_series_repair.end_at<EXCLUDED.end_at;

-- name: ClaimRetainedSeriesRepair :one
WITH candidate AS (
    SELECT slo_id,definition_revision,measurement_revision,calculation_version
    FROM public.almanac_slo_series_repair
    WHERE cursor_at<end_at AND attempts<5 AND next_attempt_at<=sqlc.arg(now)::timestamptz
      AND (lease_until IS NULL OR lease_until<=sqlc.arg(now)::timestamptz)
    ORDER BY next_attempt_at,slo_id LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE public.almanac_slo_series_repair w
SET attempts=w.attempts+1,lease_epoch=w.lease_epoch+1,lease_until=sqlc.arg(lease_until)::timestamptz
FROM candidate c WHERE (w.slo_id,w.definition_revision,w.measurement_revision,w.calculation_version)
    =(c.slo_id,c.definition_revision,c.measurement_revision,c.calculation_version)
RETURNING w.*;

-- name: AdvanceRetainedSeriesRepair :execrows
UPDATE public.almanac_slo_series_repair SET cursor_at=$7,attempts=0,lease_until=NULL,last_error=NULL,
    next_attempt_at=$8,
    coherent_through=CASE WHEN $7>=end_at AND sqlc.narg(coherent_source_generation)::bigint IS NOT NULL
                         THEN $7 ELSE coherent_through END,
    coherent_source_generation=CASE WHEN $7>=end_at AND sqlc.narg(coherent_source_generation)::bigint IS NOT NULL
                                   THEN sqlc.narg(coherent_source_generation)::bigint ELSE coherent_source_generation END
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND calculation_version=$4
  AND version=$5 AND lease_epoch=$6;

-- name: DeferRetainedSeriesRepair :exec
UPDATE public.almanac_slo_series_repair SET lease_until=NULL,attempts=GREATEST(attempts-1,0),next_attempt_at=$6,
    last_error=sqlc.narg(last_error)::text
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND calculation_version=$4 AND lease_epoch=$5;

-- name: FailRetainedSeriesRepair :exec
UPDATE public.almanac_slo_series_repair SET lease_until=NULL,
    next_attempt_at=$6::timestamptz+power(2,LEAST(attempts,5))*interval '1 second',last_error='retained_series_repair_failed'
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND calculation_version=$4 AND lease_epoch=$5;

-- name: PublishRetainedSloPointBatch :many
WITH requested AS (
    SELECT unnest(sqlc.arg(evaluation_ats)::timestamptz[]) AS evaluation_at,
           unnest(sqlc.arg(payloads)::text[]) AS payload
), accepted AS (
    INSERT INTO public.almanac_slo_series_points
        (slo_id,definition_revision,measurement_revision,calculation_version,evaluation_at,source_generation,published_at,payload)
    SELECT sqlc.arg(slo_id),sqlc.arg(definition_revision),sqlc.arg(measurement_revision),sqlc.arg(calculation_version),
           r.evaluation_at,sqlc.arg(source_generation),sqlc.arg(published_at),r.payload::jsonb FROM requested r
    ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,evaluation_at)
    DO UPDATE SET source_generation=EXCLUDED.source_generation,published_at=EXCLUDED.published_at,payload=EXCLUDED.payload
    WHERE almanac_slo_series_points.source_generation<EXCLUDED.source_generation
    RETURNING slo_id,definition_revision,measurement_revision,calculation_version,evaluation_at,source_generation,published_at
), resolutions AS (
    SELECT 3600::integer AS seconds UNION ALL SELECT 86400::integer
), wanted AS (
    SELECT a.slo_id,a.definition_revision,a.measurement_revision,a.calculation_version,r.seconds,
           to_timestamp(floor((extract(epoch FROM a.evaluation_at)-0.000001)/r.seconds)*r.seconds) AS start_at,
           max(a.source_generation) AS source_generation,max(a.published_at) AS published_at
    FROM accepted a CROSS JOIN resolutions r
    GROUP BY a.slo_id,a.definition_revision,a.measurement_revision,a.calculation_version,r.seconds,start_at
)
INSERT INTO public.almanac_slo_series_tile_work
    (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,next_attempt_at)
SELECT slo_id,definition_revision,measurement_revision,calculation_version,seconds,start_at,start_at+seconds*interval '1 second',source_generation,
       CASE WHEN seconds=86400 THEN GREATEST(published_at,start_at+interval '1 day') ELSE published_at END FROM wanted
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
DO UPDATE SET version=almanac_slo_series_tile_work.version+1,
              source_generation=GREATEST(almanac_slo_series_tile_work.source_generation,EXCLUDED.source_generation),
              next_attempt_at=LEAST(almanac_slo_series_tile_work.next_attempt_at,EXCLUDED.next_attempt_at)
RETURNING *;
