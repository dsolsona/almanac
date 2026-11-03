ALTER TABLE public.almanac_slo_history_blocks
    ADD COLUMN payload_bytes bigint CHECK (payload_bytes IS NULL OR payload_bytes>=0);

CREATE FUNCTION public.almanac_history_payload_metadata() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE payload_text text;
BEGIN
    payload_text := NEW.payload::text;
    NEW.payload_bytes := octet_length(payload_text);
    -- Keep existing digests on edits so payload corruption remains detectable.
    IF TG_OP='INSERT' AND NEW.payload_digest IS NULL THEN
        NEW.payload_digest := sha256(convert_to(payload_text,'UTF8'));
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER almanac_history_payload_metadata BEFORE INSERT OR UPDATE OF payload,payload_bytes
ON public.almanac_slo_history_blocks FOR EACH ROW EXECUTE FUNCTION public.almanac_history_payload_metadata();

CREATE OR REPLACE FUNCTION public.almanac_account_derived_cache() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE change_bytes bigint := 0;
BEGIN
    IF TG_OP<>'DELETE' THEN
        IF TG_TABLE_NAME='almanac_slo_history_blocks' THEN
            change_bytes:=coalesce(NEW.payload_bytes,octet_length(NEW.payload::text));
        ELSE
            change_bytes:=octet_length(NEW.payload::text);
        END IF;
    END IF;
    IF TG_OP<>'INSERT' THEN
        IF TG_TABLE_NAME='almanac_slo_history_blocks' THEN
            change_bytes:=change_bytes-coalesce(OLD.payload_bytes,octet_length(OLD.payload::text));
        ELSE
            change_bytes:=change_bytes-octet_length(OLD.payload::text);
        END IF;
    END IF;
    IF change_bytes<>0 THEN
        UPDATE public.almanac_derived_cache_usage SET payload_bytes=payload_bytes+change_bytes
        WHERE singleton AND payload_bytes+change_bytes BETWEEN 0 AND 2147483648;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='54000',MESSAGE='derived cache budget exhausted';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
