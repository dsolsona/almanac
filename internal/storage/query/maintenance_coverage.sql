-- name: ReadMaintenanceCoverageIdentity :one
SELECT b.slo_id, b.target_revision, b.phase, d.cadence_seconds,
       current_setting('transaction_isolation')::text AS isolation
FROM public.almanac_maintenance_builds b
JOIN public.almanac_slos s ON s.id=b.slo_id
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
WHERE b.id=sqlc.arg(build_id);

-- name: ReadMaintenanceCoverageRanks :many
SELECT a.at::timestamptz AS at, c.starts_at, c.ends_at, coalesce(c.cumulative_us,0)::bigint AS cumulative_us
FROM unnest(sqlc.arg(anchors)::timestamptz[]) AS a(at)
LEFT JOIN LATERAL (
    SELECT starts_at, ends_at, cumulative_us
    FROM public.almanac_maintenance_coverage
    WHERE build_id=sqlc.arg(build_id) AND starts_at<a.at
    ORDER BY starts_at DESC LIMIT 1
) c ON true
ORDER BY a.at;
