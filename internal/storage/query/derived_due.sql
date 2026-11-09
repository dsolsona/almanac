-- name: ReadDerivedDue :one
WITH aggregate_due AS (
    SELECT min(GREATEST(w.next_attempt_at,w.lease_until,sqlc.arg(now)::timestamptz)) AS at
    FROM public.almanac_derived_dirty_ranges w
    WHERE (w.attempts<5 OR w.failing_since IS NULL OR w.failing_since>sqlc.arg(now)::timestamptz-interval '7 days')
      AND w.last_error IS DISTINCT FROM 'retention_expired'
), current_due AS (
    SELECT min(CASE
        WHEN f.slo_id IS NOT NULL AND f.attempts>=5 AND f.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' THEN NULL
        WHEN g.id IS NULL OR g.definition_revision<>d.revision OR g.measurement_revision<>d.measurement_revision
          OR g.calculation_version<>sqlc.arg(calculation_version)
          OR g.source_generation<>coalesce(source.generation,0)
          OR g.retention_start<>to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
          OR g.evaluated_through<to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
          OR EXISTS (SELECT 1 FROM public.almanac_slo_extrema e
             WHERE e.slo_id=s.id AND e.definition_revision=d.revision AND e.measurement_revision=d.measurement_revision
               AND e.calculation_version=sqlc.arg(calculation_version) AND e.exclusion_revision=0 AND e.resolution_seconds=3600
               AND e.range_start>=date_trunc('hour',(g.evaluated_through-interval '7 days') AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
               AND e.range_end<=date_trunc('hour',g.evaluated_through AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
               AND e.published_at>coalesce((p.payload#>>'{_budget_cache,published_at}')::timestamptz,'-infinity'::timestamptz))
        THEN GREATEST(sqlc.arg(now)::timestamptz,f.next_attempt_at)
        ELSE GREATEST(to_timestamp((floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)+1)*d.cadence_seconds),f.next_attempt_at)
        END) AS at
    FROM public.almanac_slos s
    JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
    LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
    LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
    LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
    LEFT JOIN public.almanac_slo_current_failures f ON f.slo_id=s.id AND f.definition_revision=d.revision
      AND f.measurement_revision=d.measurement_revision AND f.calculation_version=sqlc.arg(calculation_version)
), schedule AS (
    SELECT aggregate_due.at AS aggregate_at,current_due.at AS current_at,
           CASE WHEN EXISTS(SELECT 1 FROM public.almanac_slos) THEN sqlc.arg(metrics_at)::timestamptz END AS metrics_at
    FROM aggregate_due,current_due
)
SELECT coalesce(aggregate_at<=sqlc.arg(now)::timestamptz,false)::boolean AS aggregate_due,
       coalesce(current_at<=sqlc.arg(now)::timestamptz,false)::boolean AS current_due,
       coalesce(metrics_at<=sqlc.arg(now)::timestamptz,false)::boolean AS metrics_due,
       GREATEST(sqlc.arg(now)::timestamptz,LEAST(sqlc.arg(now)::timestamptz+interval '1 second',aggregate_at,current_at,metrics_at))::timestamptz AS next_due
FROM schedule;

-- name: ReadExtremaDue :one
WITH active AS (
    SELECT w.* FROM public.almanac_slo_extrema_cursor w
    JOIN public.almanac_slos s ON s.id=w.slo_id AND s.definition_revision=w.definition_revision
    WHERE w.calculation_version=sqlc.arg(calculation_version)
), cursor_due AS (
    SELECT min(CASE
        -- The claim query fences expired final-attempt leases before applying backoff.
        WHEN w.attempts>=5 AND w.lease_until IS NOT NULL THEN GREATEST(sqlc.arg(now)::timestamptz,w.lease_until)
        WHEN w.attempts>=5 AND w.failing_since<=sqlc.arg(now)::timestamptz-interval '7 days' THEN NULL
        WHEN w.built_through<date_trunc('hour',sqlc.arg(now)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
          OR w.dirty_from<LEAST(w.dirty_to,date_trunc('hour',sqlc.arg(now)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
        THEN GREATEST(sqlc.arg(now)::timestamptz,w.next_attempt_at,w.lease_until)
        ELSE GREATEST((date_trunc('hour',sqlc.arg(now)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour',w.next_attempt_at,w.lease_until)
        END) AS at FROM active w
), missing AS (
    SELECT EXISTS (
        SELECT 1 FROM public.almanac_slos s
        JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
        WHERE NOT EXISTS (SELECT 1 FROM active w WHERE w.slo_id=s.id AND w.definition_revision=d.revision AND w.measurement_revision=d.measurement_revision)
    ) AS ensure
)
SELECT missing.ensure::boolean AS ensure_cursors_due,
       coalesce(cursor_due.at<=sqlc.arg(now)::timestamptz,false)::boolean AS extrema_due,
       GREATEST(sqlc.arg(now)::timestamptz,LEAST(sqlc.arg(now)::timestamptz+interval '1 second',cursor_due.at,
           CASE WHEN missing.ensure THEN sqlc.arg(now)::timestamptz END))::timestamptz AS next_due
FROM cursor_due,missing;

-- name: ReadDerivedMaintenanceDue :one
WITH cleanup AS (
    SELECT min(at) AS at FROM (
        SELECT min(range_end)+interval '365 days' AS at FROM public.almanac_slo_extrema
        UNION ALL SELECT min(g.published_at)+interval '5 minutes 1 microsecond'
            FROM public.almanac_slo_derived_generations g
            WHERE NOT EXISTS (SELECT 1 FROM public.almanac_slo_current_projections p WHERE p.generation_id=g.id)
    ) expiry
), retention AS (
    SELECT min(GREATEST(next_attempt_at,lease_until,sqlc.arg(now)::timestamptz)) AS at
    FROM public.almanac_retention_work WHERE state IN ('previewed','blocked')
), schedule AS (
    SELECT cleanup.at AS cleanup_at,retention.at AS retention_at,
       CASE WHEN EXISTS(SELECT 1 FROM public.almanac_slis) THEN sqlc.arg(discovery_at)::timestamptz END AS discovery_at,
       CASE WHEN EXISTS(SELECT 1 FROM public.almanac_slis) THEN sqlc.arg(partitions_at)::timestamptz END AS partitions_at,
       CASE WHEN EXISTS(SELECT 1 FROM public.almanac_slos) THEN sqlc.arg(consistency_at)::timestamptz END AS consistency_at
    FROM cleanup,retention
)
SELECT coalesce(cleanup_at<=sqlc.arg(now)::timestamptz,false)::boolean AS cleanup_due,
       coalesce(retention_at<=sqlc.arg(now)::timestamptz,false)::boolean AS retention_due,
       coalesce(discovery_at<=sqlc.arg(now)::timestamptz,false)::boolean AS discovery_due,
       coalesce(partitions_at<=sqlc.arg(now)::timestamptz,false)::boolean AS partitions_due,
       coalesce(consistency_at<=sqlc.arg(now)::timestamptz,false)::boolean AS consistency_due,
       GREATEST(sqlc.arg(now)::timestamptz,LEAST(sqlc.arg(now)::timestamptz+interval '1 second',cleanup_at,retention_at,discovery_at,partitions_at,consistency_at))::timestamptz AS next_due
FROM schedule;
