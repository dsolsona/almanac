WITH pending AS MATERIALIZED (
 SELECT o.sli_id,o.measurement_revision,o.interval_start,o.recorded_at,d.polling_interval_seconds
 FROM public.almanac_sli_observations o
 JOIN public.almanac_sli_measurements m ON m.sli_id=o.sli_id AND m.revision=o.measurement_revision
 JOIN public.almanac_datasources d ON d.id=m.datasource_id
 WHERE o.sli_id=$1 AND o.ingestion_generation=0
), sources AS (
 INSERT INTO public.almanac_sli_source_generations(sli_id,measurement_revision,generation,updated_at)
 SELECT sli_id,measurement_revision,count(*),max(recorded_at) FROM pending GROUP BY sli_id,measurement_revision
 ON CONFLICT(sli_id,measurement_revision) DO UPDATE
 SET generation=almanac_sli_source_generations.generation+EXCLUDED.generation,
     updated_at=GREATEST(almanac_sli_source_generations.updated_at,EXCLUDED.updated_at)
 RETURNING sli_id,measurement_revision,generation
), numbered AS (
 SELECT p.*,s.generation-count(*) OVER(PARTITION BY p.sli_id,p.measurement_revision)
   +row_number() OVER(PARTITION BY p.sli_id,p.measurement_revision ORDER BY p.interval_start) AS generation
 FROM pending p JOIN sources s USING(sli_id,measurement_revision)
), marked AS (
 UPDATE public.almanac_sli_observations o SET ingestion_generation=n.generation
 FROM numbered n WHERE o.sli_id=n.sli_id AND o.measurement_revision=n.measurement_revision AND o.interval_start=n.interval_start
 RETURNING o.sli_id,o.measurement_revision,o.interval_start,o.recorded_at,n.polling_interval_seconds,o.ingestion_generation
), hours AS (
 SELECT sli_id,measurement_revision,date_trunc('hour',interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS hour,
   max(ingestion_generation) AS generation,max(interval_start+polling_interval_seconds*interval '1 second') AS interval_end,
   min(recorded_at) AS next_attempt_at,max(recorded_at) AS updated_at
 FROM marked GROUP BY sli_id,measurement_revision,hour
), dirty AS (
 INSERT INTO public.almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
 SELECT sli_id,measurement_revision,generation,hour,GREATEST(hour+interval '1 hour',interval_end),next_attempt_at,updated_at FROM hours
 ON CONFLICT(sli_id,measurement_revision,range_start) DO UPDATE
 SET generation=EXCLUDED.generation,range_end=GREATEST(almanac_derived_dirty_ranges.range_end,EXCLUDED.range_end),
   next_attempt_at=LEAST(almanac_derived_dirty_ranges.next_attempt_at,EXCLUDED.next_attempt_at),
   updated_at=GREATEST(almanac_derived_dirty_ranges.updated_at,EXCLUDED.updated_at),attempts=0,failing_since=NULL
), affected AS (
 SELECT sli_id,measurement_revision,min(date_trunc('hour',interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS first_hour,
   max(date_trunc('hour',interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS last_hour,
   min(recorded_at) AS next_attempt_at,count(*) AS observations
 FROM marked GROUP BY sli_id,measurement_revision
)
UPDATE public.almanac_slo_extrema_cursor w
SET dirty_from=LEAST(w.dirty_from,a.first_hour),
 dirty_to=GREATEST(w.dirty_to,a.last_hour+(GREATEST(d.rolling_window_seconds,21600)+3600)*interval '1 second'),
 version=w.version+a.observations,attempts=0,failing_since=NULL,last_error=NULL,
 next_attempt_at=LEAST(w.next_attempt_at,a.next_attempt_at)
FROM affected a JOIN public.almanac_slo_definitions d ON d.sli_id=a.sli_id AND d.measurement_revision=a.measurement_revision
JOIN public.almanac_slos s ON s.id=d.slo_id AND s.definition_revision=d.revision
WHERE w.slo_id=d.slo_id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision;
