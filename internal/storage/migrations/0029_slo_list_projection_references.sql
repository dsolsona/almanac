-- List snapshots pin immutable payload identities, rather than copying every
-- matching calculation into each viewer's cursor snapshot.
CREATE INDEX almanac_derived_exhausted_sources_idx ON public.almanac_derived_dirty_ranges(sli_id,measurement_revision,range_start,range_end)
    WHERE attempts>=5;

ALTER TABLE public.almanac_slo_current_projections ADD COLUMN budget_history_state text
    GENERATED ALWAYS AS (coalesce(payload->>'budget_history_state','preparing')) STORED;
CREATE INDEX almanac_slo_current_budget_due_idx ON public.almanac_slo_current_projections(slo_id)
    WHERE budget_history_state<>'ready';
CREATE INDEX almanac_slo_current_payload_digest_idx ON public.almanac_slo_current_projections(payload_digest);

CREATE TABLE public.almanac_slo_projection_payloads (
    digest bytea PRIMARY KEY CHECK (octet_length(digest)=32),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    urgency text NOT NULL CHECK (urgency IN ('needs_attention','watch','healthy')),
    payload_bytes integer NOT NULL CHECK (payload_bytes BETWEEN 1 AND 16777216),
    published_at timestamptz NOT NULL,
    CHECK (digest=sha256(convert_to(payload::text,'UTF8')))
);
CREATE INDEX almanac_slo_projection_payloads_expiry_idx ON public.almanac_slo_projection_payloads(published_at);

CREATE FUNCTION public.almanac_archive_slo_projection() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload_digest=sha256(convert_to(NEW.payload::text,'UTF8')) THEN
        INSERT INTO public.almanac_slo_projection_payloads(digest,payload,urgency,payload_bytes,published_at)
        SELECT NEW.payload_digest,NEW.payload,NEW.urgency,octet_length(NEW.payload::text),g.published_at
        FROM public.almanac_slo_derived_generations g WHERE g.id=NEW.generation_id
        ON CONFLICT (digest) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_archive_current_slo_projection
AFTER INSERT OR UPDATE OF payload,payload_digest ON public.almanac_slo_current_projections
FOR EACH ROW EXECUTE FUNCTION public.almanac_archive_slo_projection();

INSERT INTO public.almanac_slo_projection_payloads(digest,payload,urgency,payload_bytes,published_at)
SELECT p.payload_digest,p.payload,p.urgency,octet_length(p.payload::text),g.published_at
FROM public.almanac_slo_current_projections p
JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
WHERE p.payload_digest=sha256(convert_to(p.payload::text,'UTF8'))
ON CONFLICT (digest) DO NOTHING;

ALTER TABLE public.almanac_slo_overview_snapshot_items
    ADD COLUMN projection_digest bytea REFERENCES public.almanac_slo_projection_payloads(digest),
    ADD COLUMN projection_current boolean NOT NULL DEFAULT true;
CREATE INDEX almanac_slo_overview_snapshot_projection_idx ON public.almanac_slo_overview_snapshot_items(projection_digest)
    WHERE projection_digest IS NOT NULL;
