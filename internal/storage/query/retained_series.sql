-- name: PublishRetainedSloPoint :execrows
INSERT INTO public.almanac_slo_series_points
    (slo_id,definition_revision,measurement_revision,calculation_version,evaluation_at,
     source_generation,published_at,payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,evaluation_at)
DO UPDATE SET source_generation=EXCLUDED.source_generation,published_at=EXCLUDED.published_at,payload=EXCLUDED.payload
WHERE almanac_slo_series_points.source_generation<EXCLUDED.source_generation;

-- name: ReadRetainedSloPoint :one
SELECT source_generation,published_at,payload
FROM public.almanac_slo_series_points
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3
  AND exclusion_revision=0 AND calculation_version=$4 AND evaluation_at=$5;

