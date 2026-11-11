CREATE TABLE public.almanac_session_policy (
    singleton boolean PRIMARY KEY CHECK (singleton),
    idle_minutes integer NOT NULL CHECK (idle_minutes BETWEEN 1 AND 720),
    version integer NOT NULL CHECK (version > 0)
);

INSERT INTO public.almanac_session_policy (singleton, idle_minutes, version)
VALUES (true, 480, 1);

-- Preserve the timeout of sessions issued before the policy existed.
ALTER TABLE public.almanac_sessions
    ADD COLUMN idle_minutes integer NOT NULL DEFAULT 30 CHECK (idle_minutes BETWEEN 1 AND 720);
ALTER TABLE public.almanac_sessions ALTER COLUMN idle_minutes DROP DEFAULT;
