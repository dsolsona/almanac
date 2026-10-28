ALTER TABLE public.almanac_sli_measurements
    ADD COLUMN last_attempted_interval_end timestamptz;

UPDATE public.almanac_sli_measurements AS measurement
SET last_attempted_interval_end = history.last_end
FROM (
    SELECT sli_id, measurement_revision, max(interval_end) AS last_end
    FROM (
        SELECT sli_id, measurement_revision, interval_end FROM public.almanac_sli_coverage
        UNION ALL
        SELECT sli_id, measurement_revision, interval_end FROM public.almanac_sli_execution_attempts
    ) AS entries
    GROUP BY sli_id, measurement_revision
) AS history
WHERE history.sli_id = measurement.sli_id AND history.measurement_revision = measurement.revision;
