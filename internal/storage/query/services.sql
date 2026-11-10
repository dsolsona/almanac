-- name: SetServiceCatalogMetadata :exec
UPDATE public.almanac_services SET metadata=$2 WHERE id=$1;

-- name: UpdateServiceCatalog :one
UPDATE public.almanac_services SET name=$2,display_name=$3,metadata=$4,version=version+1,updated_at=$5
WHERE id=$1 AND version=$6 AND archived_at IS NULL RETURNING *;

-- name: CountActiveServiceIDs :one
SELECT count(*)::bigint FROM public.almanac_services WHERE id=ANY($1::uuid[]) AND archived_at IS NULL;

-- name: ReplaceServiceDependenciesDelete :exec
DELETE FROM public.almanac_service_dependencies WHERE service_id=$1;

-- name: ReplaceServiceDependenciesInsert :exec
INSERT INTO public.almanac_service_dependencies(service_id,dependency_id)
SELECT $1,unnest($2::uuid[]);

-- name: ReadServiceCatalogFields :many
SELECT s.id,s.metadata,
 coalesce((SELECT jsonb_agg(jsonb_build_object('id',target.id,'name',target.name,'display_name',target.display_name,'owner',coalesce(target.metadata->>'owner','')) ORDER BY target.created_at,target.id)
   FROM public.almanac_service_dependencies edge JOIN public.almanac_services target ON target.id=edge.dependency_id
   WHERE edge.service_id=s.id AND target.archived_at IS NULL),'[]'::jsonb)::jsonb AS dependencies,
 coalesce((SELECT jsonb_agg(jsonb_build_object('id',source.id,'name',source.name,'display_name',source.display_name,'owner',coalesce(source.metadata->>'owner','')) ORDER BY source.created_at,source.id)
   FROM (SELECT subject.* FROM public.almanac_service_dependencies edge JOIN public.almanac_services subject ON subject.id=edge.service_id
     WHERE edge.dependency_id=s.id AND subject.archived_at IS NULL ORDER BY subject.created_at,subject.id LIMIT 200) source),'[]'::jsonb)::jsonb AS used_by,
 (SELECT count(*)::bigint FROM public.almanac_service_dependencies edge JOIN public.almanac_services source ON source.id=edge.service_id WHERE edge.dependency_id=s.id AND source.archived_at IS NULL) AS used_by_count
FROM public.almanac_services s WHERE s.id=ANY($1::uuid[]) AND s.archived_at IS NULL;

-- name: ServiceArchiveBlockers :one
SELECT coalesce((SELECT jsonb_agg(DISTINCT definition.slo_id) FROM public.almanac_slo_definitions definition WHERE definition.service_id=$1),'[]'::jsonb)::jsonb AS slos,
 coalesce((SELECT jsonb_agg(source.id) FROM public.almanac_service_dependencies edge JOIN public.almanac_services source ON source.id=edge.service_id WHERE edge.dependency_id=$1 AND source.archived_at IS NULL),'[]'::jsonb)::jsonb AS services;

-- name: ArchiveService :execrows
UPDATE public.almanac_services SET archived_at=$2,updated_at=$2,version=version+1 WHERE id=$1 AND version=$3 AND archived_at IS NULL;

-- name: ListServiceSloStates :many
SELECT d.service_id,
 CASE WHEN coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
    AND g.calculation_version=sqlc.arg(calculation_version)
    AND g.source_generation=coalesce(source.generation,0)
    AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
    AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND coalesce((p.payload->>'_current_valid_until')::timestamptz,'-infinity'::timestamptz)>sqlc.arg(now)::timestamptz
    AND p.payload#>>'{current,quality,state}'='complete'
    AND p.payload#>>'{current,quality,freshness}'='fresh'
    AND p.payload#>>'{current,performance,provisional}'='false',false)
 THEN coalesce(p.payload#>>'{current,performance,state}','unknown') ELSE 'unknown' END::text AS state
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
WHERE d.service_id=ANY(sqlc.arg(service_ids)::uuid[]);

-- name: ListCatalogServices :many
SELECT * FROM public.almanac_services s
WHERE archived_at IS NULL
 AND (s.created_at,s.id)>(sqlc.arg(after_time),sqlc.arg(after_id)::uuid)
 AND (sqlc.arg(query_text)::text='' OR strpos(lower(s.name||' '||s.display_name||' '||s.id::text||' '||s.metadata::text||' '||coalesce((SELECT string_agg(label.key||' '||value.item||' '||label.key||':'||value.item,' ') FROM jsonb_each(coalesce(s.metadata->'labels','{}'::jsonb)) label CROSS JOIN LATERAL jsonb_array_elements_text(label.value) value(item)),'')),lower(sqlc.arg(query_text)::text))>0)
 AND (sqlc.arg(owner_name)::text='' OR coalesce(nullif(s.metadata->>'owner',''),'Unassigned')=sqlc.arg(owner_name)::text)
 AND (sqlc.arg(coverage)::text='all' OR EXISTS(SELECT 1 FROM public.almanac_slo_definitions d JOIN public.almanac_slos slo ON slo.id=d.slo_id AND slo.definition_revision=d.revision WHERE d.service_id=s.id)=(sqlc.arg(coverage)::text='with'))
ORDER BY s.created_at,s.id LIMIT sqlc.arg(page_limit);

-- name: CatalogServiceCounts :one
SELECT count(*)::bigint AS total,count(*) FILTER(WHERE EXISTS(SELECT 1 FROM public.almanac_slo_definitions d JOIN public.almanac_slos slo ON slo.id=d.slo_id AND slo.definition_revision=d.revision WHERE d.service_id=s.id))::bigint AS with_slos,
 coalesce(jsonb_agg(DISTINCT coalesce(nullif(metadata->>'owner',''),'Unassigned')),'[]'::jsonb)::jsonb AS owners
FROM public.almanac_services s WHERE archived_at IS NULL;

-- name: CatalogFilteredServiceCount :one
SELECT count(*)::bigint FROM public.almanac_services s WHERE archived_at IS NULL
 AND (sqlc.arg(query_text)::text='' OR strpos(lower(s.name||' '||s.display_name||' '||s.id::text||' '||s.metadata::text||' '||coalesce((SELECT string_agg(label.key||' '||value.item||' '||label.key||':'||value.item,' ') FROM jsonb_each(coalesce(s.metadata->'labels','{}'::jsonb)) label CROSS JOIN LATERAL jsonb_array_elements_text(label.value) value(item)),'')),lower(sqlc.arg(query_text)::text))>0)
 AND (sqlc.arg(owner_name)::text='' OR coalesce(nullif(s.metadata->>'owner',''),'Unassigned')=sqlc.arg(owner_name)::text)
 AND (sqlc.arg(coverage)::text='all' OR EXISTS(SELECT 1 FROM public.almanac_slo_definitions d JOIN public.almanac_slos slo ON slo.id=d.slo_id AND slo.definition_revision=d.revision WHERE d.service_id=s.id)=(sqlc.arg(coverage)::text='with'));

-- name: ListSortedCatalogServices :many
WITH scope AS MATERIALIZED (
SELECT * FROM public.almanac_services s
WHERE archived_at IS NULL
 AND (sqlc.arg(query_text)::text='' OR strpos(lower(s.name||' '||s.display_name||' '||s.id::text||' '||s.metadata::text||' '||coalesce((SELECT string_agg(label.key||' '||value.item||' '||label.key||':'||value.item,' ') FROM jsonb_each(coalesce(s.metadata->'labels','{}'::jsonb)) label CROSS JOIN LATERAL jsonb_array_elements_text(label.value) value(item)),'')),lower(sqlc.arg(query_text)::text))>0)
 AND (sqlc.arg(owner_name)::text='' OR coalesce(nullif(s.metadata->>'owner',''),'Unassigned')=sqlc.arg(owner_name)::text)
 AND (sqlc.arg(coverage)::text='all' OR EXISTS(SELECT 1 FROM public.almanac_slo_definitions d JOIN public.almanac_slos slo ON slo.id=d.slo_id AND slo.definition_revision=d.revision WHERE d.service_id=s.id)=(sqlc.arg(coverage)::text='with'))
), states AS MATERIALIZED (
SELECT d.service_id,
 CASE WHEN coalesce(g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
    AND g.calculation_version=sqlc.arg(calculation_version)
    AND g.source_generation=coalesce(source.generation,0)
    AND g.retention_start=to_timestamp(ceil(extract(epoch FROM (sqlc.arg(now)::timestamptz-interval '365 days'))/d.cadence_seconds)*d.cadence_seconds)
    AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM sqlc.arg(now)::timestamptz)/d.cadence_seconds)*d.cadence_seconds)
             AND coalesce((p.payload->>'_current_valid_until')::timestamptz,'-infinity'::timestamptz)>sqlc.arg(now)::timestamptz
    AND p.payload#>>'{current,quality,state}'='complete'
    AND p.payload#>>'{current,quality,freshness}'='fresh'
    AND p.payload#>>'{current,performance,provisional}'='false',false)
 THEN coalesce(p.payload#>>'{current,performance,state}','unknown') ELSE 'unknown' END::text AS state
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
LEFT JOIN public.almanac_slo_current_projections p ON p.slo_id=s.id
LEFT JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
WHERE d.service_id=ANY(SELECT id FROM scope) AND sqlc.arg(sort_key)::text='slo_status'
), keys AS MATERIALIZED (
 SELECT s.id,
 CASE sqlc.arg(sort_key)::text
  WHEN 'slo_status' THEN coalesce((SELECT min(CASE state WHEN 'violated' THEN 0 WHEN 'met' THEN 2 ELSE 1 END) FROM states WHERE service_id=s.id),3)
  WHEN 'slo_count' THEN (SELECT count(*) FROM almanac_slo_definitions d JOIN almanac_slos objective ON objective.id=d.slo_id AND objective.definition_revision=d.revision WHERE d.service_id=s.id)
  WHEN 'dependency_count' THEN (SELECT count(*) FROM almanac_service_dependencies edge WHERE edge.service_id=s.id)
  ELSE 0 END::bigint AS sort_number,
 CASE sqlc.arg(sort_key)::text
  WHEN 'name' THEN lower(s.display_name)
  WHEN 'owner' THEN lower(coalesce(nullif(s.metadata->>'owner',''),'Unassigned'))
  ELSE '' END::text AS sort_text
 FROM scope s
)
SELECT sqlc.embed(catalog_record),keys.sort_number,keys.sort_text
FROM keys JOIN public.almanac_services catalog_record ON catalog_record.id=keys.id
WHERE NOT sqlc.arg(has_cursor)::boolean
 OR (NOT sqlc.arg(descending)::boolean AND (keys.sort_number,keys.sort_text,catalog_record.id)>(sqlc.arg(after_number)::bigint,sqlc.arg(after_text)::text,sqlc.arg(after_id)::uuid))
 OR (sqlc.arg(descending)::boolean AND (keys.sort_number,keys.sort_text,catalog_record.id)<(sqlc.arg(after_number)::bigint,sqlc.arg(after_text)::text,sqlc.arg(after_id)::uuid))
ORDER BY
 CASE WHEN NOT sqlc.arg(descending)::boolean THEN keys.sort_number END ASC,
 CASE WHEN NOT sqlc.arg(descending)::boolean THEN keys.sort_text END ASC,
 CASE WHEN NOT sqlc.arg(descending)::boolean THEN catalog_record.id END ASC,
 CASE WHEN sqlc.arg(descending)::boolean THEN keys.sort_number END DESC,
 CASE WHEN sqlc.arg(descending)::boolean THEN keys.sort_text END DESC,
 CASE WHEN sqlc.arg(descending)::boolean THEN catalog_record.id END DESC
LIMIT sqlc.arg(page_limit);
