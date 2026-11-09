-- name: ListCanonicalRetentionPartitions :many
WITH partitions AS (
    SELECT child.oid,child.relname::text AS partition_name,parent.relname::text AS parent_name,
           CASE WHEN child.relkind='r' THEN regexp_match(pg_get_expr(child.relpartbound,child.oid),
               '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$') ELSE NULL END AS bounds,
           CASE WHEN child.relkind='r' THEN pg_total_relation_size(child.oid) ELSE 0 END::bigint AS bytes,
           CASE WHEN child.reltuples>=0 THEN child.reltuples::bigint ELSE -1 END::bigint AS estimated_rows
    FROM pg_catalog.pg_inherits inheritance
    JOIN pg_catalog.pg_class parent ON parent.oid=inheritance.inhparent
    JOIN pg_catalog.pg_namespace parent_schema ON parent_schema.oid=parent.relnamespace
    JOIN pg_catalog.pg_class child ON child.oid=inheritance.inhrelid
    JOIN pg_catalog.pg_namespace child_schema ON child_schema.oid=child.relnamespace
    WHERE parent_schema.nspname='public' AND child_schema.nspname='public' AND child.relkind IN ('r','p','f')
      AND parent.relname='almanac_sli_observations'
)
SELECT oid::bigint AS relation_id,partition_name,parent_name,(bounds[1])::timestamptz AS range_start,
       (bounds[2])::timestamptz AS range_end,bytes,estimated_rows
FROM partitions ORDER BY parent_name,partition_name LIMIT 1000;
