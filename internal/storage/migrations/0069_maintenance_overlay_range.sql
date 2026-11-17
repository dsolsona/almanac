-- Seek by overlap before checking current revision and SLO membership.
CREATE INDEX almanac_maintenance_revision_range_idx
ON public.almanac_maintenance_exclusion_revisions
USING gist (tstzrange(starts_at, ends_at, '[)'))
WHERE NOT tombstone;
