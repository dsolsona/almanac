import { useCallback, useEffect, useId, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, Pager } from '../components/ui/index.ts';
import { SloFailure } from '../slos/client.ts';
import { maintenanceClient, type ImpactPage, type MaintenanceSelector } from './client.ts';

const readDeadline = 30_000;

const snapshotChanged = (error: unknown) => error instanceof SloFailure && error.code === 'maintenance_snapshot_changed';

type ReadImpact = (limit: number, cursor: string, signal: AbortSignal) => Promise<ImpactPage>;

export function MaintenanceTime({ value, showZone = true }: { value: string; showZone?: boolean }) {
  return <time dateTime={value}>{value.replace('T', ' ').replace(/(\.0+)?Z$/, showZone ? ' UTC' : '')}</time>;
}

export function MaintenanceImpactPreview({ scope, context, onReady }: {
  scope: MaintenanceSelector;
  context: AuthContext;
  onReady: (scope: MaintenanceSelector | undefined) => void;
}) {
  const read = useCallback<ReadImpact>(async (limit, cursor, signal) => {
    try {
      const page = await maintenanceClient.preview(scope, context, limit, cursor, signal);
      if (page.ready && !signal.aborted) onReady(scope);
      return page;
    } catch (error) {
      if (snapshotChanged(error) && !signal.aborted) onReady(undefined);
      throw error;
    }
  }, [scope, context, onReady]);
  return <MaintenanceImpact preview read={read} />;
}

export function MaintenanceWindowImpact({ id, revision, refreshVersion }: { id: string; revision: string; refreshVersion: number }) {
  const read = useCallback<ReadImpact>((limit, cursor, signal) =>
    maintenanceClient.impact(id, revision, limit, cursor, signal), [id, revision]);
  return <MaintenanceImpact read={read} refreshVersion={refreshVersion} />;
}

function MaintenanceImpact({ read, preview = false, refreshVersion }: { read: ReadImpact; preview?: boolean; refreshVersion?: number }) {
  const headingId = useId();
  const [size, setSize] = useState(10);
  const [cursors, setCursors] = useState(['']);
  const [page, setPage] = useState<ImpactPage>();
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<'load' | 'changed'>();
  const [refresh, setRefresh] = useState(0);
  const [usedPager, setUsedPager] = useState(false);
  const cursor = cursors.at(-1) ?? '';

  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError(undefined);
    const deadline = window.setTimeout(() => {
      controller.abort();
      setError('load');
      setBusy(false);
    }, readDeadline);
    read(size, cursor, controller.signal).then(page => {
      if (!controller.signal.aborted) setPage(page);
    }).catch(error => {
      if (!controller.signal.aborted) setError(snapshotChanged(error) ? 'changed' : 'load');
    }).finally(() => {
      window.clearTimeout(deadline);
      if (!controller.signal.aborted) setBusy(false);
    });
    return () => {
      window.clearTimeout(deadline);
      controller.abort();
    };
  }, [read, size, cursor, refresh, refreshVersion]);

  function changePage(next: string[], nextSize = size) {
    setPage(undefined);
    setError(undefined);
    setBusy(true);
    setUsedPager(true);
    setCursors(next);
    setSize(nextSize);
  }

  return <section className={preview ? 'maintenance-impact' : 'maintenance-impact maintenance-window-impact'} aria-labelledby={headingId} aria-busy={busy || undefined}>
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
    {(!error || (!preview && error === 'load')) && page?.ready && <>
      <p role="status" className="ui-hint">
        {new Intl.NumberFormat().format(BigInt(page.total))} {page.total === '1' ? 'affected SLO' : 'affected SLOs'} · Inventory as of <MaintenanceTime value={page.inventory_at} />
      </p>
      {!preview && page.items.length > 0 && <p className="ui-hint">An SLO can have multiple participation periods.</p>}
      {page.items.length > 0 && (preview
        ? <ul className="maintenance-impact-list" role="list">{page.items.map(item => <li key={item.slo_id}>
          {item.slo_name}<div className="ui-hint">{item.service_name}</div>
        </li>)}</ul>
        : <div className="ui-resource-panel">
          <table className="ui-list-table ui-resource-table" aria-label="Recorded SLO participation">
            <thead><tr><th scope="col">SLO</th><th scope="col">From (UTC)</th><th scope="col">Until (UTC)</th></tr></thead>
            <tbody>{page.items.map(item => <tr key={`${item.slo_id}:${item.start}`}>
              <td data-label="SLO"><a href={`/slos/${item.slo_id}`} data-app-route>{item.slo_name}</a><span className="ui-list-table__sub">{item.service_name}</span></td>
              <td data-label="From (UTC)">{item.start && <MaintenanceTime value={item.start} showZone={false} />}</td>
              <td data-label="Until (UTC)">{item.end && <MaintenanceTime value={item.end} showZone={false} />}</td>
            </tr>)}</tbody>
          </table>
        </div>)}
    </>}
    {(usedPager || page?.next_cursor || (page?.ready && BigInt(page.total) > 10n)) &&
      <Pager id={`${headingId}-size`} label="Affected SLO pages" position={`Page ${cursors.length}`} pageSize={size} pageSizes={[10, 20]}
        onPageSizeChange={value => value !== size && changePage([''], value)}
        hasPrevious={cursors.length > 1} hasNext={Boolean(page?.next_cursor) && !error} busy={busy}
        onPrevious={() => changePage(cursors.slice(0, -1))}
        onNext={() => page?.next_cursor && changePage([...cursors, page.next_cursor])} />}
  </section>;
}
