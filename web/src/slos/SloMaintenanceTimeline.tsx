import { useEffect, useState } from 'react';
import { Button } from '../components/ui/index.ts';
import { pointTime } from '../components/charts/pointInspection.ts';
import { readSloMaintenance, type OverlayPage } from './maintenanceOverlay.ts';

type Layer = { key: string; page?: OverlayPage; error?: boolean };

export function openSloMaintenanceList() {
  const list = document.querySelector<HTMLDetailsElement>('#slo-maintenance-periods');
  if (!list) return;
  list.open = true;
  list.scrollIntoView({ block: 'start' });
  list.querySelector('summary')?.focus({ preventScroll: true });
}

export function useSloMaintenance(id: string, start: string | undefined, end: string | undefined, refresh: number) {
  const [layer, setLayer] = useState<Layer>();
  const key = JSON.stringify([id, start, end, refresh]);
  useEffect(() => {
    if (!start || !end || Date.parse(start) >= Date.parse(end)) return;
    const controller = new AbortController();
    readSloMaintenance(id, start, end, '', controller.signal)
      .then(page => { if (!controller.signal.aborted) setLayer({ key, page }); })
      .catch(() => { if (!controller.signal.aborted) setLayer({ key, error: true }); });
    return () => controller.abort();
  }, [key, id, start, end]);
  return { page: layer?.key === key ? layer.page : undefined, error: layer?.key === key && layer.error };
}

type ListProps = {
  id: string;
  start: string;
  end: string;
  zone: 'UTC' | 'local';
  page?: OverlayPage;
  error?: boolean;
};

export function SloMaintenanceList({ id, start, end, zone, page, error }: ListProps) {
  if (error) return <p className="slo-detail-notice" role="status">Maintenance periods could not be loaded. Refresh objective history to try again.</p>;
  return <>
    {page?.pending && <p className="slo-detail-notice" role="status">Some maintenance periods are still being prepared.</p>}
    {!!page?.items.length && <MaintenancePages key={JSON.stringify([id, start, end, page.source, page.items[0], page.next_cursor])}
      id={id} start={start} end={end} zone={zone} first={page} />}
  </>;
}

function MaintenancePages({ id, start, end, zone, first }: Omit<ListProps, 'page' | 'error'> & { first: OverlayPage }) {
  const [loaded, setLoaded] = useState<{ cursor: string; result: OverlayPage }>();
  const [cursors, setCursors] = useState(['']);
  const [error, setError] = useState(false);
  const cursor = cursors.at(-1) ?? '';
  const page = cursor ? loaded?.cursor === cursor ? loaded.result : undefined : first;

  useEffect(() => {
    if (!cursor || page) return;
    const controller = new AbortController();
    setError(false);
    readSloMaintenance(id, start, end, cursor, controller.signal, first.source)
      .then(result => { if (!controller.signal.aborted) setLoaded({ cursor, result }); })
      .catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => controller.abort();
  }, [cursor, page, id, start, end, first.source]);

  return <details id="slo-maintenance-periods" className="slo-detail-disclosure">
    <summary>Maintenance periods{first.overflow ? <span>{first.overflow.count} grouped on the graphs</span>
      : first.next_cursor ? <span>First 200 shown on the graphs</span> : <span>{first.items.length} in this period</span>}</summary>
    <div>
      <p className="small">Shaded periods mark scheduled exclusions for this SLO. Each graph reports its own calculation and data coverage.</p>
      {first.overflow && <p className="small">The grouped marker spans the additional windows. Open a window to see its exact bounds.</p>}
      {first.next_cursor && !first.overflow && <p className="small">Browse additional windows here or choose a shorter graph range.</p>}
      {error && !page && <p role="status">This page could not be loaded. Go back and try again.</p>}
      {!page && !error && <p role="status">Loading maintenance periods…</p>}
      {page && <ul className="slo-maintenance-events">{page.items.map(window => <li key={`${window.id}:${window.start}:${window.end}`}>
        <a href={`/maintenance/${window.id}`} data-app-route>{window.title}</a>
        <span><time dateTime={window.start}>{pointTime(window.start, zone, true)}</time> to <time dateTime={window.end}>{pointTime(window.end, zone, true)}</time></span>
      </li>)}</ul>}
      {(cursors.length > 1 || page?.next_cursor) && <div className="slo-maintenance-page-actions">
        <Button variant="outline" disabled={cursors.length === 1} onClick={() => setCursors(current => current.slice(0, -1))}>Previous windows</Button>
        <Button variant="outline" disabled={!page?.next_cursor} onClick={() => page?.next_cursor && setCursors(current => [...current, page.next_cursor!])}>Next windows</Button>
      </div>}
    </div>
  </details>;
}
