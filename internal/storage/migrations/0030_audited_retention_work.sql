CREATE INDEX almanac_derived_retention_ranges_idx ON public.almanac_derived_dirty_ranges(range_start,range_end);

CREATE TABLE public.almanac_retention_work (
    id uuid PRIMARY KEY,
    scope_digest bytea NOT NULL UNIQUE CHECK (octet_length(scope_digest)=32),
    range_start timestamptz NOT NULL,
    range_end timestamptz NOT NULL,
    cutoff timestamptz NOT NULL,
    preview jsonb NOT NULL CHECK (jsonb_typeof(preview)='object'),
    state text NOT NULL DEFAULT 'previewed' CHECK (state IN ('previewed','blocked','applied','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    next_attempt_at timestamptz NOT NULL,
    last_error text,
    previewed_at timestamptz NOT NULL,
    applied_at timestamptz,
    CHECK (range_start<range_end AND range_end<=cutoff),
    CHECK ((state='applied')=(applied_at IS NOT NULL))
);
CREATE INDEX almanac_retention_work_due_idx ON public.almanac_retention_work(next_attempt_at,id)
    WHERE state IN ('previewed','blocked') AND attempts<5;
