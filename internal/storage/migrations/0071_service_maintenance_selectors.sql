ALTER TABLE public.almanac_maintenance_exclusion_revisions
    ADD COLUMN scope_kind text NOT NULL DEFAULT 'slos' CHECK (scope_kind IN ('slos', 'services', 'all_services'));

CREATE TABLE public.almanac_maintenance_service_selectors (
    exclusion_id uuid NOT NULL,
    revision bigint NOT NULL,
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    PRIMARY KEY (exclusion_id, revision, service_id),
    FOREIGN KEY (exclusion_id, revision)
        REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id, revision)
);
CREATE INDEX almanac_maintenance_service_selector_service_idx
    ON public.almanac_maintenance_service_selectors(service_id, exclusion_id, revision);
CREATE TRIGGER almanac_maintenance_service_selector_immutable
    BEFORE UPDATE ON public.almanac_maintenance_service_selectors
    FOR EACH ROW EXECUTE FUNCTION public.almanac_immutable_maintenance_revision();

CREATE INDEX almanac_maintenance_cutoff_revision_idx
    ON public.almanac_maintenance_exclusion_revisions(exclusion_id, recorded_at DESC, revision DESC);
CREATE INDEX almanac_maintenance_all_services_range_idx
    ON public.almanac_maintenance_exclusion_revisions USING gist (tstzrange(starts_at, ends_at, '[)'))
    WHERE scope_kind = 'all_services' AND NOT tombstone;

-- The desired revision changes atomically with the public window. A worker
-- can advance this UUID cursor only while publishing that same revision.
CREATE TABLE public.almanac_service_maintenance_projection (
    exclusion_id uuid PRIMARY KEY REFERENCES public.almanac_maintenance_exclusions(id),
    desired_revision bigint NOT NULL,
    through_slo uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    complete boolean NOT NULL DEFAULT false,
    FOREIGN KEY (exclusion_id, desired_revision)
        REFERENCES public.almanac_maintenance_exclusion_revisions(exclusion_id, revision)
);
