import { useEffect, useId, useRef, useState, type MouseEvent } from 'react';
import { Button, Dialog, PageHeader, Pager, RefreshButton, SearchField, SelectField, StatusChip, TextField, Toolbar, ToolbarSpacer, resourceRowClick } from '../components/ui/index.ts';
import './occurrence-list.css';

export type AlertLifecycle = 'firing' | 'acknowledged' | 'resolved' | 'disabled' | 'superseded';
type SloSummary = { id: string; name: string; service: string; href: string };
export type AlertMatch = { id: string; name: string; shortSeconds: number; longSeconds: number; shortBurn: string; longBurn: string; threshold: string };
export type AlertOccurrenceRow = {
  id: string; version: string; policyName: string; href: string; slo: SloSummary;
  state: AlertLifecycle; severity: 'low' | 'medium' | 'high'; openedAt: string; detectedAt: string;
  closedAt?: string; terminalCause?: string; reconstructed?: boolean; detectedAfterRecovery?: boolean;
  acknowledged?: { actor: string; at: string };
  evidence: { at: string; matches: AlertMatch[]; unavailableReason?: string };
  processing: { state: 'current' } | { state: 'waiting' | 'behind'; dueAt: string; reason: string };
};
type PageBounds = { total: number; start: number; end: number; hasPrevious: boolean; hasNext: boolean };
export type AlertListPage = PageBounds & (
  { kind: 'flat'; items: AlertOccurrenceRow[] } |
  { kind: 'slo'; groups: { slo: SloSummary; total: number; firing: number; items: AlertOccurrenceRow[] }[] }
);
export type AlertListFilters = { policyName: string; sloId: string; severity: string; state: string; evidence: string; minimumAge: string };
export const emptyAlertListFilters = (): AlertListFilters => ({ policyName: '', sloId: '', severity: '', state: '', evidence: '', minimumAge: '' });
type FilterSection = 'all' | 'alert' | 'slo' | 'state' | 'evidence' | 'age';
type Props = {
  page: AlertListPage; pageSize: number; filters: AlertListFilters; view: 'active' | 'history'; grouped: boolean; now: string;
  slos: { id: string; label: string }[]; canAcknowledge: boolean; newPolicyHref: string;
  onFiltersChange: (filters: AlertListFilters) => void; onGroupChange: (grouped: boolean) => void;
  onPageSizeChange: (size: number) => void; onPrevious: () => void; onNext: () => void; onRefresh: () => void;
  onOpen: (href: string) => void; onAcknowledge: (id: string, expectedVersion: string) => void;
  acknowledgement?: { id: string; pending: boolean; error?: string };
  gaps?: { id: string; start: string; end: string; reason: string }[];
  busy?: boolean; error?: string;
};
const labels: Record<AlertLifecycle, string> = { firing: 'Firing', acknowledged: 'Acknowledged', resolved: 'Resolved', disabled: 'Disabled', superseded: 'Superseded' };
const titles: Record<FilterSection, string> = { all: 'Filter alerts', alert: 'Filter alert', slo: 'Filter SLO', state: 'Filter state', evidence: 'Filter evidence', age: 'Filter duration' };
function windowLabel(seconds: number) { return seconds % 86400 === 0 ? `${seconds / 86400}d` : seconds % 3600 === 0 ? `${seconds / 3600}h` : `${seconds / 60}m`; }
function clock(value: string) { return new Date(value).toISOString().slice(11, 16) + ' UTC'; }
function fullTime(value: string) { return new Date(value).toISOString().replace('T', ' ').replace(/:00\.000Z$/, ' UTC'); }
function duration(start: string, end: string) {
  const minutes = Math.max(0, Math.floor((Date.parse(end) - Date.parse(start)) / 60000));
  if (minutes < 1) return '<1m';
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h ${minutes % 60}m` : `${Math.floor(hours / 24)}d ${hours % 24}h`;
}
function Match({ match }: { match: AlertMatch }) {
  return <div className="alert-match">
    <span className="alert-match__name">{match.name}</span>
    <span className="alert-match__windows"><span>{windowLabel(match.shortSeconds)} <strong>{match.shortBurn}×</strong></span><span>{windowLabel(match.longSeconds)} <strong>{match.longBurn}×</strong></span></span>
    <small>Both ≥ {match.threshold}× burn</small>
  </div>;
}

// Presentation consumes one bounded server page. It never calculates condition truth,
// applies global filters locally, or changes lifecycle before its owner supplies new data.
export function AlertOccurrenceList(props: Props) {
  const { page, filters, acknowledgement, view } = props;
  const id = useId();
  const trigger = useRef<HTMLElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const viewLinks = useRef(new Map<string, HTMLAnchorElement>());
  const requested = useRef<{ id: string; button: HTMLButtonElement } | null>(null);
  useEffect(() => {
    const request = requested.current;
    if (!request) return;
    if (acknowledgement?.id === request.id && acknowledgement.error) { requested.current = null; return; }
    const rows = page.kind === 'flat' ? page.items : page.groups.flatMap(group => group.items);
    if (rows.find(item => item.id === request.id)?.state === 'firing') return;
    // A confirmed state change can replace/remove the focused action. Restore a
    // useful target without stealing focus from somebody already working elsewhere.
    if (document.activeElement === document.body || document.activeElement === request.button) {
      (viewLinks.current.get(request.id) ?? heading.current)?.focus();
    }
    requested.current = null;
  }, [page, acknowledgement]);
  const [section, setSection] = useState<FilterSection | null>(null);
  const [draft, setDraft] = useState(filters);
  function openFilter(event: MouseEvent<HTMLButtonElement>, next: FilterSection) {
    trigger.current = event.currentTarget; setDraft({ ...filters }); setSection(next);
  }
  const filterCount = Object.values(filters).filter(Boolean).length;
  function column(label: string, target: FilterSection, selected: boolean) {
    return <th scope="col"><button type="button" className={`alert-column-filter${selected ? ' is-filtered' : ''}`} aria-label={`Filter ${label}`} aria-haspopup="dialog" onClick={event => openFilter(event, target)}>{label}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 3h12L9 9v4l-2-1V9z" /></svg></button></th>;
  }
  function row(item: AlertOccurrenceRow) {
    const action = acknowledgement?.id === item.id ? acknowledgement : undefined;
    return <tr key={item.id} onClick={event => resourceRowClick(event, () => props.onOpen(item.href))}>
      <td><a href={item.href} data-app-route className="alert-name">{item.policyName}</a><span className={`alert-severity alert-severity--${item.severity}`}>{item.severity[0]!.toUpperCase() + item.severity.slice(1)} severity</span></td>
      <td><a href={item.slo.href} data-app-route>{item.slo.name}</a><span className="alert-secondary">{item.slo.service}</span></td>
      <td><StatusChip tone={item.state === 'firing' ? 'breached' : item.state === 'acknowledged' ? 'stale' : item.state === 'resolved' ? 'healthy' : 'none'} label={labels[item.state]} />
        {item.acknowledged && <span className="alert-secondary" title={`Acknowledged ${fullTime(item.acknowledged.at)}`}>{item.acknowledged.actor}</span>}
        {item.terminalCause && <span className="alert-secondary">{item.terminalCause}</span>}
      </td>
      <td className="alert-evidence">{item.evidence.matches[0] && <Match match={item.evidence.matches[0]} />}
        {item.evidence.matches.length > 1 && <details className="alert-other-matches" onClick={event => event.stopPropagation()}><summary>{item.evidence.matches.length - 1} more matching {item.evidence.matches.length === 2 ? 'condition' : 'conditions'}</summary>{item.evidence.matches.slice(1).map(match => <Match key={match.id} match={match} />)}</details>}
        {item.evidence.unavailableReason && <span className="alert-evidence-warning">{item.evidence.unavailableReason}</span>}
        {item.processing.state !== 'current' && <span className="alert-processing"><strong>{item.processing.reason}</strong><span title={fullTime(item.processing.dueAt)}>Evaluation due {clock(item.processing.dueAt)}</span></span>}
        {(item.processing.state !== 'current' || item.evidence.unavailableReason) && <span className="alert-secondary" title={fullTime(item.evidence.at)}>Last evaluated {clock(item.evidence.at)}</span>}
        {item.detectedAfterRecovery && <span className="alert-secondary">Detected after recovery</span>}
        {item.reconstructed && <span className="alert-secondary">Reconstructed during catch-up</span>}
      </td>
      <td><time className="alert-duration" dateTime={item.openedAt} title={`Opened ${fullTime(item.openedAt)}`}>{duration(item.openedAt, item.closedAt ?? props.now)}</time><span className="alert-secondary">{clock(item.openedAt)}</span>{item.closedAt && <details className="alert-closure"><summary>Closed {clock(item.closedAt)}</summary><time dateTime={item.closedAt}>{fullTime(item.closedAt)}</time></details>}{view === 'history' && <span className="alert-secondary" title={fullTime(item.detectedAt)}>Detected {clock(item.detectedAt)}</span>}</td>
      <td className="alert-row-action">{props.canAcknowledge && item.state === 'firing' && view === 'active' ? <Button variant="outline" pending={action?.pending} disabled={acknowledgement?.pending && !action} aria-label={`Acknowledge ${item.policyName}`} onClick={event => { requested.current = { id: item.id, button: event.currentTarget }; props.onAcknowledge(item.id, item.version); }}>Acknowledge</Button> : <a href={item.href} data-app-route ref={node => { if (node) viewLinks.current.set(item.id, node); else viewLinks.current.delete(item.id); }} className="ui-button ui-button--link" aria-label={`View ${item.policyName}`}>View →</a>}
        {action?.error && <p role="alert" className="ui-error">{action.error}</p>}
      </td>
    </tr>;
  }
  const hasRows = page.kind === 'flat' ? page.items.length > 0 : page.groups.some(group => group.items.length > 0);
  const includes = (part: FilterSection) => section === 'all' || section === part;
  const field = (key: keyof AlertListFilters, value: string) => setDraft(previous => ({ ...previous, [key]: value }));
  return <div className="alert-occurrence-list">
    <PageHeader headingRef={heading} title={view === 'active' ? 'Alerts' : 'Alert history'} actions={<a className="ui-button ui-button--primary" href={props.newPolicyHref} data-app-route>New alert policy</a>} />
    <Toolbar>
      <SearchField inputRef={search} id={`${id}-search`} label="Search alerts" placeholder="Search alerts…" value={filters.policyName} onChange={event => props.onFiltersChange({ ...filters, policyName: event.target.value })} />
      <ToolbarSpacer /><Button variant={filterCount ? 'selected' : 'ghost'} aria-haspopup="dialog" onClick={event => openFilter(event, 'all')}>Filters{filterCount > 0 ? ` · ${filterCount}` : ''}</Button>
      <Button variant={props.grouped ? 'selected' : 'ghost'} aria-pressed={props.grouped} onClick={() => props.onGroupChange(!props.grouped)}>Group by SLO</Button>
      {filterCount > 0 && <Button variant="link" onClick={() => { props.onFiltersChange(emptyAlertListFilters()); search.current?.focus(); }}>Clear filters</Button>}
      <RefreshButton label="Refresh alerts" busy={props.busy} onClick={props.onRefresh} />
    </Toolbar>
    {props.error && <p role="alert" className="alert-list-notice">{props.error}</p>}
    {props.gaps?.map(gap => <div key={gap.id} className="alert-list-notice"><strong>Unreconstructed evaluation gap</strong><p>{fullTime(gap.start)} – {fullTime(gap.end)} · {gap.reason}</p></div>)}
    <div className="alert-table-scroll" role="region" aria-label="Alert records" tabIndex={0} aria-busy={props.busy || undefined}>
      <table className="ui-list-table alert-table" aria-label={view === 'active' ? 'Active alerts' : 'Alert history'}><thead><tr>
        {column('Alert', 'alert', !!(filters.policyName || filters.severity))}{column('SLO', 'slo', !!filters.sloId)}{column('State', 'state', !!filters.state)}{column(view === 'active' ? "Why it’s open" : 'Evidence', 'evidence', !!filters.evidence)}{column(view === 'active' ? 'Open for' : 'Duration', 'age', !!filters.minimumAge)}<th scope="col"><span className="sr-only">Actions</span></th>
      </tr></thead>
      {hasRows ? page.kind === 'flat' ? <tbody>{page.items.map(row)}</tbody> : page.groups.map(group => <tbody key={group.slo.id}>
        <tr className="alert-group"><th colSpan={6} scope="rowgroup"><a href={group.slo.href} data-app-route>{group.slo.name}</a><span>{group.total} {group.total === 1 ? 'alert' : 'alerts'} · {group.firing} firing</span></th></tr>{group.items.map(row)}
      </tbody>) : <tbody><tr><td colSpan={6} className="alert-empty">{props.busy ? 'Loading alerts…' : props.error ? 'Alerts could not be loaded. Refresh to try again.' : filterCount ? 'No alerts match these filters.' : view === 'active' ? 'No active alerts.' : 'No alert occurrences in this history.'}</td></tr></tbody>}
      </table>
    </div>
    <Pager id={`${id}-page-size`} label="Alert pages" position={`${page.total ? `${page.start}–${page.end}` : '0'} of ${page.total} ${page.total === 1 ? 'alert' : 'alerts'}`} pageSize={props.pageSize} onPageSizeChange={props.onPageSizeChange} hasPrevious={page.hasPrevious} hasNext={page.hasNext} onPrevious={props.onPrevious} onNext={props.onNext} busy={props.busy} />
    <Dialog open={section !== null} onClose={() => setSection(null)} title={titles[section ?? 'all']} triggerRef={trigger}>
      <form className="alert-filter-fields" onSubmit={event => { event.preventDefault(); props.onFiltersChange(draft); setSection(null); }}>
        <div className="alert-filter-controls">
        {includes('alert') && <><TextField id={`${id}-name`} label="Alert name" value={draft.policyName} onChange={event => field('policyName', event.target.value)} /><SelectField id={`${id}-severity`} label="Severity" value={draft.severity} options={[{ value: '', label: 'All severities' }, ...['high', 'medium', 'low'].map(value => ({ value, label: value[0]!.toUpperCase() + value.slice(1) }))]} onChange={event => field('severity', event.target.value)} /></>}
        {includes('slo') && <SelectField id={`${id}-slo`} label="SLO" value={draft.sloId} options={[{ value: '', label: 'All SLOs' }, ...props.slos.map(slo => ({ value: slo.id, label: slo.label }))]} onChange={event => field('sloId', event.target.value)} />}
        {includes('state') && <SelectField id={`${id}-state`} label="State" value={draft.state} options={[{ value: '', label: 'All states' }, ...Object.entries(labels).filter(([value]) => view === 'active' ? ['firing', 'acknowledged'].includes(value) : !['firing', 'acknowledged'].includes(value)).map(([value, label]) => ({ value, label }))]} onChange={event => field('state', event.target.value)} />}
        {includes('evidence') && <SelectField id={`${id}-evidence`} label="Evidence" value={draft.evidence} options={[{ value: '', label: 'All evidence' }, { value: 'matching', label: 'Matching conditions' }, { value: 'unavailable', label: 'Unavailable recovery evidence' }, { value: 'waiting', label: 'Waiting for calculation' }]} onChange={event => field('evidence', event.target.value)} />}
        {includes('age') && <SelectField id={`${id}-age`} label="Minimum duration" value={draft.minimumAge} options={[{ value: '', label: 'Any duration' }, { value: '900', label: '15 minutes' }, { value: '1800', label: '30 minutes' }, { value: '3600', label: '1 hour' }]} onChange={event => field('minimumAge', event.target.value)} />}
        </div>
        <div className="alert-filter-actions"><Button variant="link" onClick={() => setDraft(emptyAlertListFilters())}>Reset all</Button><Button variant="outline" onClick={() => setSection(null)}>Cancel</Button><Button type="submit">Apply</Button></div>
      </form>
    </Dialog>
  </div>;
}
