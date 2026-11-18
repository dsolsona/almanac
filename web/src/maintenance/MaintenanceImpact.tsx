import { useCallback, useEffect, useId, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, Pager } from '../components/ui/index.ts';
import { SloFailure } from '../slos/client.ts';
import { maintenanceClient, type ImpactPage, type MaintenanceSelector } from './client.ts';

type ReadImpact = (limit: number, cursor: string, signal: AbortSignal) => Promise<ImpactPage>;

const utc = (instant: string) => instant.replace('T', ' ').replace(/(\.0+)?Z$/, ' UTC');

export function MaintenanceImpactPreview({ scope, context, onReady }: {
  scope: MaintenanceSelector;
  context: AuthContext;
  onReady: (scope: MaintenanceSelector) => void;
}) {
  const read = useCallback<ReadImpact>(async (limit, cursor, signal) => {
    const page = await maintenanceClient.preview(scope, context, limit, cursor, signal);
    if (page.ready && !signal.aborted) onReady(scope);
    return page;
  }, [scope, context, onReady]);
  return <MaintenanceImpact preview read={read} />;
}

export function MaintenanceWindowImpact({ id, revision }: { id: string; revision: string }) {
  const read = useCallback<ReadImpact>((limit, cursor, signal) =>
    maintenanceClient.impact(id, revision, limit, cursor, signal), [id, revision]);
  return <MaintenanceImpact read={read} />;
}

function MaintenanceImpact({ read, preview = false }: { read: ReadImpact; preview?: boolean }) {
  const headingId = useId();
  const [size, setSize] = useState(10);
  const [cursors, setCursors] = useState(['']);
  const [page, setPage] = useState<ImpactPage>();
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<'load' | 'changed'>();
  const [refresh, setRefresh] = useState(0);
  const cursor = cursors.at(-1) ?? '';

  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError(undefined);
    read(size, cursor, controller.signal).then(page => {
      if (!controller.signal.aborted) setPage(page);
    }).catch(error => {
      if (!controller.signal.aborted) setError(error instanceof SloFailure && error.code === 'maintenance_snapshot_changed' ? 'changed' : 'load');
    }).finally(() => {
      if (!controller.signal.aborted) setBusy(false);
    });
    return () => controller.abort();
  }, [read, size, cursor, refresh]);

  function changePage(next: string[], nextSize = size) {
    setPage(undefined);
    setError(undefined);
    setBusy(true);
    setCursors(next);
    setSize(nextSize);
  }

  return <section className="maintenance-impact" aria-labelledby={headingId} aria-busy={busy || undefined}>
    {preview ? <h3 id={headingId}>Affected SLO preview</h3> : <h2 id={headingId}>Affected SLOs</h2>}
    {busy && !page && <span role="status" className="sr-only">Loading affected SLOs</span>}
    {error && <p role="alert" className="ui-error">{error === 'changed' ? 'Maintenance inventory changed. Reload from the first page.' : 'Affected SLOs could not be loaded.'}</p>}
    {!busy && (error || page?.ready === false) && <Button variant="outline" onClick={() => {
      if (error === 'changed') changePage(['']);
      setRefresh(value => value + 1);
    }}>
      {error === 'changed' ? 'Reload affected SLOs' : error ? 'Retry affected SLOs' : 'Refresh affected SLOs'}
    </Button>}
    {!error && page?.ready === false && <p role="status" className="ui-hint">Affected SLOs are being prepared.</p>}
    {!error && page?.ready && <>
      <p role="status" className="ui-hint">
        {new Intl.NumberFormat().format(BigInt(page.total))} {page.total === '1' ? 'affected SLO' : 'affected SLOs'} · Inventory as of <time dateTime={page.inventory_at}>{utc(page.inventory_at)}</time>
      </p>
      {!preview && page.items.length > 0 && <p className="ui-hint">Each SLO is counted once and can appear in more than one participation period.</p>}
      {page.items.length > 0 && <ul className="maintenance-impact-list">{page.items.map(item => <li key={`${item.slo_id}:${item.start ?? ''}`}>
        {preview ? item.slo_name : <a href={`/slos/${item.slo_id}`} data-app-route>{item.slo_name}</a>}
        <div className="ui-hint">{item.service_name}</div>
        {item.start && item.end && <div className="ui-hint"><time dateTime={item.start}>{utc(item.start)}</time> to <time dateTime={item.end}>{utc(item.end)}</time></div>}
      </li>)}</ul>}
    </>}
    {(cursors.length > 1 || page?.next_cursor || (page?.ready && BigInt(page.total) > 10n)) &&
      <Pager id={`${headingId}-size`} label="Affected SLO pages" position={`Page ${cursors.length}`} pageSize={size} pageSizes={[10, 20]}
        onPageSizeChange={value => value !== size && changePage([''], value)}
        hasPrevious={cursors.length > 1} hasNext={Boolean(page?.next_cursor) && !error} busy={busy}
        onPrevious={() => changePage(cursors.slice(0, -1))}
        onNext={() => page?.next_cursor && changePage([...cursors, page.next_cursor])} />}
  </section>;
}
