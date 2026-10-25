CREATE TABLE public.almanac_installation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    installation_id uuid NOT NULL DEFAULT gen_random_uuid(),
    credential_epoch uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.almanac_installation (singleton) VALUES (true);
