ALTER TABLE public.almanac_diagnostics
    ADD COLUMN result_digest bytea CHECK (
        result_digest IS NULL OR
        (octet_length(result_digest) = 32 AND result IS NOT NULL)
    );
