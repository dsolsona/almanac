-- name: FindPreparedSloHistory :one
-- Select metadata before detoasting/hashing payloads from unrelated generations.
WITH candidates AS MATERIALIZED (
SELECT b.payload, b.payload_digest, g.id AS generation_id, g.published_at
FROM public.almanac_slo_history_blocks b
JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.slo_id=$1 AND g.definition_revision=$2 AND g.measurement_revision=$3
  AND g.calculation_version=sqlc.arg(calculation_version)
  AND g.retention_start=sqlc.arg(retention_start)
  AND g.published_at>=sqlc.arg(now)::timestamptz-interval '5 minutes'
  AND g.source_generation=$4 AND g.evaluated_through=$5
  AND b.range_start=$6 AND b.range_end=$7 AND b.burn_window_seconds=$8
  AND b.kind=sqlc.arg(kind)
)
SELECT payload,generation_id,published_at FROM candidates
WHERE payload->'history_preparation' IS NULL
  AND payload_digest=sha256(convert_to(payload::text,'UTF8'))
ORDER BY published_at DESC LIMIT 1;

-- name: DeleteCorruptPreparedSloHistory :execrows
DELETE FROM public.almanac_slo_history_blocks WHERE generation_id=$1;

-- Exact table pages read only one bounded, immutable prepared generation.
-- name: ReadPreparedSloValuePage :one
SELECT g.id,g.source_generation,g.definition_revision,g.measurement_revision,
       g.calculation_version,g.evaluated_through,g.published_at,g.retention_start,
       jsonb_array_length(b.payload #> sqlc.arg(series_path)::text[])::integer AS total_values,
       coalesce((
           SELECT jsonb_agg(e.value ORDER BY e.ordinality)
           FROM jsonb_array_elements(b.payload #> sqlc.arg(series_path)::text[]) WITH ORDINALITY e
           WHERE e.ordinality>sqlc.arg(page_offset)::integer
             AND e.ordinality<=sqlc.arg(page_offset)::integer+sqlc.arg(page_limit)::integer
       ),'[]'::jsonb)::jsonb AS page_values
FROM public.almanac_slo_history_blocks b
JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id
WHERE g.slo_id=sqlc.arg(slo_id) AND g.id=sqlc.arg(generation_id) AND b.kind IN ('detail','retained_detail')
  AND g.published_at>=sqlc.arg(now)::timestamptz-interval '5 minutes'
  AND b.payload_digest=sha256(convert_to(b.payload::text,'UTF8'))
  AND g.calculation_version=sqlc.arg(calculation_version);

-- name: InsertPreparedSloHistory :exec
INSERT INTO public.almanac_slo_history_blocks
    (generation_id,range_start,range_end,burn_window_seconds,payload,kind,budget_sparkline)
VALUES ($1,$2,$3,$4,$5,$6,$7);

-- name: DeleteExpiredDerivedGenerations :execrows
DELETE FROM public.almanac_slo_derived_generations g
WHERE g.id IN (
    SELECT old.id FROM public.almanac_slo_derived_generations old
    WHERE old.published_at < $1
      AND NOT EXISTS (SELECT 1 FROM public.almanac_slo_current_projections p WHERE p.generation_id=old.id)
    ORDER BY old.published_at LIMIT 100
);
