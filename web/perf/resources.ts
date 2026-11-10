import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { psql } from '../e2e/support/stack.ts';

// These are the same relations counted by ReadDerivedMetrics, with partition children grouped
// under their canonical parent. pg_table_size includes TOAST; pg_indexes_size counts main indexes.
const canonical = ['almanac_sli_observations'];
const derived = ['almanac_sli_hourly_aggregates', 'almanac_derived_dirty_ranges',
  'almanac_sli_source_generations', 'almanac_slo_derived_generations', 'almanac_slo_current_projections',
  'almanac_slo_current_failures', 'almanac_slo_history_blocks', 'almanac_derived_cache_usage',
  'almanac_slo_extrema', 'almanac_slo_extrema_cursor'];

export function resourceSnapshot() {
  const database = JSON.parse(psql(`SELECT json_build_object(
    'at', clock_timestamp(), 'wal_lsn', pg_current_wal_insert_lsn()::text,
    'pg_stat_statements_installed', EXISTS (SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements'),
    'shared_preload_libraries', current_setting('shared_preload_libraries'),
    'compute_query_id', current_setting('compute_query_id'),
    'pg_stat_statements_track', current_setting('pg_stat_statements.track', true))`)) as {
    at: string; wal_lsn: string; pg_stat_statements_installed: boolean; shared_preload_libraries: string;
    compute_query_id: string; pg_stat_statements_track: string | null;
  };
  const preloaded = database.shared_preload_libraries.split(',').some((library) => library.trim() === 'pg_stat_statements');
  const unavailable = !preloaded ? 'pg_stat_statements requires server startup with shared_preload_libraries'
    : !database.pg_stat_statements_installed ? 'pg_stat_statements extension is not installed in the fixture database'
    : !['auto', 'on'].includes(database.compute_query_id) ? `compute_query_id=${database.compute_query_id}`
    : !database.pg_stat_statements_track || database.pg_stat_statements_track === 'none' ? 'pg_stat_statements tracking is disabled' : null;
  return {
    ...database,
    pg_stat_statements_preloaded: preloaded,
    statement_counter_unavailable_reason: unavailable,
    statements: unavailable === null ? Number(psql(`SELECT coalesce(sum(calls),0) FROM pg_stat_statements
      WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())`)) : null,
  };
}

export function relationSizes() {
  const names = [...canonical, ...derived].map((name) => `('${name}', '${canonical.includes(name) ? 'canonical' : 'derived'}')`).join(',');
  return JSON.parse(psql(`WITH roots(name, kind) AS (VALUES ${names}), leaves AS (
    SELECT roots.name, roots.kind, tree.relid FROM roots
    JOIN pg_class c ON c.oid=('public.' || roots.name)::regclass
    CROSS JOIN LATERAL pg_partition_tree(CASE WHEN c.relkind='p' THEN c.oid END) tree WHERE tree.isleaf
    UNION ALL
    SELECT roots.name, roots.kind, c.oid FROM roots JOIN pg_class c ON c.oid=('public.' || roots.name)::regclass
    WHERE c.relkind='r'
  ), allocated AS MATERIALIZED (
    -- Read each size once so concurrent growth cannot contradict the component total.
    SELECT name,kind,pg_table_size(relid) AS table_bytes,pg_indexes_size(relid) AS index_bytes FROM leaves
  ), sizes AS (
    SELECT name,kind,sum(table_bytes) AS table_bytes,sum(index_bytes) AS index_bytes,
      sum(table_bytes+index_bytes) AS total_bytes FROM allocated GROUP BY name,kind
  ) SELECT coalesce(json_agg(sizes ORDER BY kind, name), '[]'::json) FROM sizes`)) as {
    name: string; kind: string; table_bytes: number; index_bytes: number; total_bytes: number;
  }[];
}

export function resourceDelta(start: ReturnType<typeof resourceSnapshot>, end: ReturnType<typeof resourceSnapshot>) {
  const seconds = (Date.parse(end.at) - Date.parse(start.at)) / 1000;
  const wal = Number(psql(`SELECT pg_wal_lsn_diff('${end.wal_lsn}', '${start.wal_lsn}')`));
  const statements = start.statements === null || end.statements === null ? null : end.statements - start.statements;
  return { start, end, seconds, wal_bytes: wal, wal_bytes_per_hour: wal * 3600 / seconds,
    statements, statements_per_minute: statements === null ? null : statements * 60 / seconds };
}

export function ingestionBetween(start: string, end: string) {
  return Number(psql(`SELECT count(*) FROM public.almanac_sli_observations
    WHERE recorded_at >= '${start}'::timestamptz AND recorded_at < '${end}'::timestamptz`));
}

export function pauseAgent(container: string, paused: boolean) {
  if (!/^[a-f0-9]{64}$/.test(container)) throw new Error('not a performance fixture agent ID');
  const owner = execFileSync('docker', ['inspect', '--format', '{{ index .Config.Labels "almanac.e2e" }}', container], { encoding: 'utf8', stdio: 'pipe', timeout: 30_000 }).trim();
  if (owner !== 'owned') throw new Error('not an owned performance fixture agent');
  execFileSync('docker', [paused ? 'pause' : 'unpause', container], { stdio: 'pipe', timeout: 30_000 });
}

export function writeResources(evidence: unknown) {
  mkdirSync('../.local/perf', { recursive: true });
  writeFileSync('../.local/perf/resources.json', JSON.stringify(evidence, null, 2));
}
