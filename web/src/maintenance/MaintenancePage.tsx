import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { LoadingIndicator, PageHeader, Pager, RefreshButton, StatusChip, Toolbar, ToolbarSpacer, resourceRowClick } from '../components/ui/index.ts';
import type { ServerClock } from '../slos/client.ts';
import { maintenanceClient, type MaintenancePage as WindowPage, type MaintenanceWindow } from './client.ts';
import { MaintenanceTime, MaintenanceWindowImpact } from './MaintenanceImpact.tsx';
import { scopeIDs, scopeLabels } from './ScopePicker.tsx';
import { MaintenanceLifecycle } from './MaintenanceLifecycle.tsx';
import { MaintenanceScope } from './MaintenanceScope.tsx';
import { ScheduleMaintenance } from './ScheduleMaintenance.tsx';
import './maintenance.css';

const root = '/maintenance';
const format = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' });
const scheduleDate = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeZone: 'UTC' });
const timeLabel = (time: string) => format.format(new Date(time));
const errorText = (error: unknown) => error instanceof Error ? error.message : 'The request could not be completed.';

function ScheduleTime({ value }: { value: string }) {
  const clock = value.slice(11, -1).replace(/\.0+$/, '').replace(/^(\d\d:\d\d):00$/, '$1');
  return <time dateTime={value} title={value}>
    <strong>{clock}</strong>
    <span>{scheduleDate.format(new Date(value))}</span>
  </time>;
}

function windowStatus(window: MaintenanceWindow, now: number) {
  if (window.tombstone) return { label: window.operation === 'cancel' ? 'Cancelled' : 'Withdrawn', tone: 'none' } as const;
  if (Date.parse(window.start) > now) return { label: 'Scheduled', tone: 'stale' } as const;
  if (Date.parse(window.end) <= now) return { label: 'Ended', tone: 'none' } as const;
  return { label: 'In progress', tone: 'warning' } as const;
}

function readWindowClock(clock?: ServerClock) {
  return clock ? clock.time + Math.max(0, performance.now() - clock.receivedAt) : Date.now();
}

function useWindowClock(clock: ServerClock | undefined, windows: MaintenanceWindow[]) {
  const [tick, setTick] = useState(0);
  const now = readWindowClock(clock);
  const next = windows.reduce((boundary, window) => {
    if (window.tombstone) return boundary;
    const start = Date.parse(window.start);
    const end = Date.parse(window.end);
    return Math.min(boundary, start > now ? start : end > now ? end : Infinity);
  }, Infinity);

  useEffect(() => {
    if (next === Infinity) return;
    const delay = next - readWindowClock(clock);
    const timer = window.setTimeout(() => setTick(value => value + 1), Math.min(2_147_483_647, Math.max(1, Math.ceil(delay))));
    return () => window.clearTimeout(timer);
  }, [clock, next, tick]);
  return now;
}

function navigate(path: string) {
  history.pushState({}, '', path);
  dispatchEvent(new PopStateEvent('popstate'));
}

function ScheduleLink() {
  return <a href="/maintenance/new" data-app-route className="ui-button ui-button--primary">Schedule maintenance</a>;
}

function WindowList({ canWrite }: { canWrite: boolean }) {
  const [size, setSize] = useState(10);
  const [cursors, setCursors] = useState(['']);
  const [page, setPage] = useState<WindowPage>();
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [clock, setClock] = useState<ServerClock>();
  const now = useWindowClock(clock, page?.items ?? []);
  const cursor = cursors.at(-1) ?? '';

  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError('');
    maintenanceClient.list(size, cursor, controller.signal).then(result => {
      if (controller.signal.aborted) return;
      setPage(result.value);
      setClock(result.serverClock);
      setBusy(false);
    }).catch(error => {
      if (controller.signal.aborted) return;
      setError(errorText(error));
      setBusy(false);
    });
    return () => controller.abort();
  }, [size, cursor, refresh]);

  function changePage(nextCursors: string[], nextSize = size) {
    setPage(undefined);
    setSize(nextSize);
    setCursors(nextCursors);
  }

  return <>
    <PageHeader title="Maintenance" actions={canWrite && <ScheduleLink />} />
    <Toolbar><ToolbarSpacer /><RefreshButton label="Refresh maintenance" busy={busy} onClick={() => setRefresh(value => value + 1)} /></Toolbar>
    {error && <p role="alert" className="ui-error">{error}</p>}
    {busy && !page && <LoadingIndicator label="Loading maintenance" />}
    {page && <div className="ui-resource-panel" aria-busy={busy || undefined}>
      {page.items.length > 0
        ? <table className="ui-list-table ui-resource-table">
          <thead><tr><th scope="col">Maintenance</th><th scope="col">Status</th><th scope="col">Scope</th><th scope="col">Starts (UTC)</th><th scope="col">Ends (UTC)</th></tr></thead>
          <tbody>{page.items.map(window => <tr key={window.id} onClick={event => resourceRowClick(event, () => navigate(`${root}/${window.id}`))}>
            <td data-label="Maintenance"><a href={`${root}/${window.id}`} data-app-route>{window.title}</a></td>
            <td data-label="Status"><StatusChip {...windowStatus(window, now)} /></td>
            <td data-label="Scope">{scopeLabels[window.scope.kind]}{window.scope.kind !== 'all_services' && ` · ${scopeIDs(window.scope).length}`}</td>
            <td data-label="Starts (UTC)"><time dateTime={window.start}>{timeLabel(window.start)}</time></td>
            <td data-label="Ends (UTC)"><time dateTime={window.end}>{timeLabel(window.end)}</time></td>
          </tr>)}</tbody>
        </table>
        : <div className="maintenance-empty"><h2>No maintenance windows</h2><p>Scheduled and past maintenance windows appear here.</p></div>}
      <Pager id="maintenance-page-size" label="Maintenance pages" position={`Page ${cursors.length}`} pageSize={size} pageSizes={[10, 20]}
        onPageSizeChange={value => value !== size && changePage([''], value)}
        hasPrevious={cursors.length > 1} hasNext={Boolean(page.next_cursor)} busy={busy}
        onPrevious={() => changePage(cursors.slice(0, -1))}
        onNext={() => page.next_cursor && changePage([...cursors, page.next_cursor])} />
    </div>}
  </>;
}

function WindowDetail({ id, revision, editing, context }: { id: string; revision: string; editing: boolean; context: AuthContext }) {
  const [result, setResult] = useState<Awaited<ReturnType<typeof maintenanceClient.get>>>();
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const detailRead = useRef<AbortController>(undefined);

  useEffect(() => {
    const controller = new AbortController();
    detailRead.current = controller;
    setError('');
    maintenanceClient.get(id, revision, controller.signal).then(result => {
      if (!controller.signal.aborted) setResult(result);
    }).catch(error => {
      if (!controller.signal.aborted) setError(errorText(error));
    });
    return () => controller.abort();
  }, [id, revision, refresh]);

  const window = result?.value;
  const now = useWindowClock(result?.serverClock, window ? [window] : []);
  const heading = useRef<HTMLHeadingElement>(null);
  const previous = window && BigInt(window.revision) - 1n;
  function acceptLifecycleRevision(next: Awaited<ReturnType<typeof maintenanceClient.get>>) {
    detailRead.current?.abort();
    setError('');
    setResult(next);
  }
  function applyLifecycleUpdate(next: Awaited<ReturnType<typeof maintenanceClient.get>>) {
    acceptLifecycleRevision(next);
    requestAnimationFrame(() => heading.current?.focus());
    setRefresh(value => value + 1);
  }
  const canEdit = context.session?.role === 'administrator' || context.session?.role === 'editor';
  const isFuture = window && !window.tombstone && Date.parse(window.start) > now;
  const [openedForEdit, setOpenedForEdit] = useState(false);
  useEffect(() => {
    if (editing && !revision && canEdit && isFuture) setOpenedForEdit(true);
  }, [editing, revision, canEdit, isFuture]);
  if (editing && result && openedForEdit && canEdit) return <ScheduleMaintenance context={context} existing={result} />;

  return <article>
    <PageHeader headingRef={heading} title={window?.title ?? 'Maintenance window'} breadcrumb={{ href: root, label: 'Maintenance', current: window?.title ?? 'Window' }}
      summary={window && <span className="maintenance-window-status">
        <StatusChip {...windowStatus(window, now)} />
        {revision && <span>Viewing revision {window.revision}</span>}
      </span>}
      actions={<>
        {!revision && !editing && canEdit && isFuture && <a data-app-route href={`${root}/${id}?edit=1`} className="ui-button ui-button--primary">Edit schedule</a>}
        {revision && window && <a data-app-route href={`${root}/${id}`} className="ui-button ui-button--outline">Latest revision</a>}
        {!revision && result && <MaintenanceLifecycle context={context} record={result.value} etag={result.etag} now={now} readNow={() => readWindowClock(result.serverClock)}
          fallbackFocus={heading} onRevisionRead={acceptLifecycleRevision} onUpdated={applyLifecycleUpdate} />}
        <RefreshButton label="Refresh maintenance window" onClick={() => setRefresh(value => value + 1)} />
      </>} />
    {error && <p className="ui-error" role="alert">{error}</p>}
    {editing && result && !isFuture && <p role="status">The window is no longer scheduled. Its current details are shown.</p>}
    {editing && result && !canEdit && <p role="status">An editor or administrator can change the schedule.</p>}
    {!window && !error && <LoadingIndicator label="Loading maintenance window" />}
    {window && <div className="maintenance-window-content">
      <section className="ui-panel maintenance-window-overview" aria-labelledby="maintenance-window-heading">
        <header><h2 id="maintenance-window-heading">Schedule</h2><span>UTC</span></header>
        <dl className="maintenance-window-times">
          <div><dt>Starts</dt><dd><ScheduleTime value={window.start} /></dd></div>
          <div><dt>Ends</dt><dd><ScheduleTime value={window.end} /></dd></div>
        </dl>
        <div className="maintenance-window-context">
          <div><h3>Reason</h3><p className="maintenance-reason">{window.reason}</p></div>
          <section className="maintenance-window-scope" aria-labelledby="maintenance-window-scope">
            <h3 id="maintenance-window-scope">Scope</h3>
            <MaintenanceScope key={window.revision} scope={window.scope} links />
          </section>
        </div>
      </section>
      {isFuture
        ? <section className="maintenance-window-impact" aria-labelledby="maintenance-pending-impact">
          <h2 id="maintenance-pending-impact">Affected SLOs</h2>
          <p className="ui-hint">Recorded SLO participation will appear when the window starts.</p>
        </section>
        : <MaintenanceWindowImpact key={window.revision} id={window.id} revision={window.revision} refreshVersion={refresh} />}
      <footer className="maintenance-window-footer">
        <details className="maintenance-disclosure">
          <summary>Revision history</summary>
          <p>Revision {window.revision} · <MaintenanceTime value={window.updated_at} /></p>
          {previous !== undefined && previous > 0n && <a data-app-route href={`${root}/${id}?revision=${previous}`}>Previous revision</a>}
        </details>
        {!revision && result && <MaintenanceLifecycle historical context={context} record={window} etag={result.etag} now={now} readNow={() => readWindowClock(result.serverClock)}
          fallbackFocus={heading} onRevisionRead={acceptLifecycleRevision} onUpdated={applyLifecycleUpdate} />}
      </footer>
    </div>}
  </article>;
}

export function MaintenancePage({ context }: { context: AuthContext }) {
  const [route, setRoute] = useState(() => location.pathname + location.search);
  useEffect(() => {
    const onRoute = () => setRoute(location.pathname + location.search);
    addEventListener('popstate', onRoute);
    return () => removeEventListener('popstate', onRoute);
  }, []);

  const [pathname = root, query] = route.split('?');
  const id = pathname.split('/')[2];
  const canWrite = context.session?.role === 'administrator' || context.session?.role === 'editor';
  const params = new URLSearchParams(query);
  const revision = params.get('revision') ?? '';

  return <div className="maintenance-page">{id === 'new'
    ? canWrite ? <ScheduleMaintenance context={context} /> : <><PageHeader title="Schedule maintenance" /><p role="alert">An editor or administrator can schedule maintenance.</p></>
    : id ? <WindowDetail key={route} id={id} context={context} revision={revision} editing={!revision && params.get('edit') === '1'} />
      : <WindowList canWrite={canWrite} />}</div>;
}
