ALTER TABLE public.almanac_services
    ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    ADD COLUMN archived_at timestamptz;

CREATE TABLE public.almanac_service_dependencies (
    service_id uuid NOT NULL REFERENCES public.almanac_services(id),
    dependency_id uuid NOT NULL REFERENCES public.almanac_services(id),
    PRIMARY KEY(service_id, dependency_id),
    CHECK(service_id<>dependency_id)
);
CREATE INDEX almanac_service_dependencies_inverse_idx
    ON public.almanac_service_dependencies(dependency_id, service_id);
CREATE INDEX almanac_services_active_page_idx
    ON public.almanac_services(created_at,id) WHERE archived_at IS NULL;
