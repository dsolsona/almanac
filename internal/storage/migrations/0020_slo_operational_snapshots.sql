CREATE TABLE public.almanac_slo_overview_snapshots (
    id uuid PRIMARY KEY,
    cursor_secret bytea NOT NULL CHECK (octet_length(cursor_secret) = 32),
    actor_id uuid NOT NULL REFERENCES public.almanac_users(id) ON DELETE CASCADE,
    session_verifier bytea NOT NULL CHECK (octet_length(session_verifier) = 32),
    role text NOT NULL CHECK (role IN ('administrator', 'editor', 'viewer')),
    query_text text NOT NULL,
    service_id uuid REFERENCES public.almanac_services(id) ON DELETE CASCADE,
    urgency text CHECK (urgency IS NULL OR urgency IN ('needs_attention', 'watch', 'healthy')),
    page_limit integer NOT NULL CHECK (page_limit BETWEEN 1 AND 200),
    group_counts jsonb NOT NULL CHECK (jsonb_typeof(group_counts) = 'object'),
    state text NOT NULL CHECK (state IN ('preparing', 'ready')),
    payload_bytes integer NOT NULL DEFAULT 0 CHECK (payload_bytes BETWEEN 0 AND 16777216),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at = created_at + interval '2 minutes')
);

CREATE INDEX almanac_slo_overview_snapshots_expiry_idx
    ON public.almanac_slo_overview_snapshots(expires_at, id);
CREATE INDEX almanac_slo_overview_snapshots_session_idx
    ON public.almanac_slo_overview_snapshots(actor_id, session_verifier, expires_at);

CREATE TABLE public.almanac_slo_overview_snapshot_items (
    snapshot_id uuid NOT NULL REFERENCES public.almanac_slo_overview_snapshots(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 9999),
    urgency text NOT NULL CHECK (urgency IN ('needs_attention', 'watch', 'healthy')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    payload_bytes integer NOT NULL CHECK (payload_bytes BETWEEN 1 AND 16777216),
    PRIMARY KEY (snapshot_id, ordinal)
);
