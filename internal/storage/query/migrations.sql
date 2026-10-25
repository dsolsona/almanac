-- name: GetMigrationHistory :many
SELECT version, filename, checksum, applied_at
FROM public.almanac_schema_migrations
ORDER BY version;

-- name: RecordMigration :exec
INSERT INTO public.almanac_schema_migrations (version, filename, checksum)
VALUES ($1, $2, $3);
