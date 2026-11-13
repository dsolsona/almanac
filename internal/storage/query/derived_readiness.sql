-- Same interior and lease semantics as ReadRetainedWindowBatch. A ready result is
-- advisory: the caller must still execute that authoritative batch in its snapshot.
-- name: ReadDerivedWindowReadiness :one
SELECT coalesce(NOT EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
      AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
),false)::boolean AS prepared,
EXISTS (
    SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=sqlc.arg(sli_id) AND d.measurement_revision=sqlc.arg(measurement_revision)
      AND sqlc.arg(full_start)::timestamptz<sqlc.arg(full_end)::timestamptz
      AND d.range_start<sqlc.arg(full_end)::timestamptz AND d.range_end>sqlc.arg(full_start)::timestamptz
      AND d.attempts>=5 AND (d.lease_until IS NULL OR d.lease_until<=sqlc.arg(now)::timestamptz)
)::boolean AS exhausted;
