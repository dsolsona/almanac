import { readFileSync } from 'node:fs';

const query = readFileSync(new URL('./alert-calculation.readiness.sql', import.meta.url), 'utf8').trim();

// Fixture-only query. It deliberately does not require live current status to be
// simultaneously current for all SLOs; workload currentness remains a separate gate.
export function publishedHistoryReadinessSQL(ids: string[], closedThrough: string): string {
  if (!ids.length || ids.length > 200 || new Set(ids).size !== ids.length ||
      ids.some((id) => !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id))) {
    throw new Error('expected 1–200 distinct fixture SLO UUIDs');
  }
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:00:00Z$/.test(closedThrough) || !Number.isFinite(Date.parse(closedThrough))) {
    throw new Error('expected an aligned UTC closed-hour cutoff');
  }
  return query.replaceAll('$1', `ARRAY[${ids.map((id) => `'${id}'`).join(',')}]::uuid[]`)
    .replaceAll('$2', `'${closedThrough}'::timestamptz`);
}
