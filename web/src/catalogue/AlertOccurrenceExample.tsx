import { useState } from 'react';
import { AlertOccurrenceList, emptyAlertListFilters, type AlertListFilters, type AlertListPage, type AlertOccurrenceRow } from '../alerts/AlertOccurrenceList.tsx';
import { Button } from '../components/ui/index.ts';

const now = '2026-11-12T10:42:00Z';
const fast = { id: 'fast', name: 'Fast burn', shortSeconds: 300, longSeconds: 3600, shortBurn: '22.8', longBurn: '18.6', threshold: '14.4' };
const sustained = { id: 'sustained', name: 'Sustained burn', shortSeconds: 1800, longSeconds: 21600, shortBurn: '12.5', longBurn: '7.4', threshold: '6' };
const slos = [
  { id: 'checkout', name: 'Checkout availability', service: 'Checkout', href: '#alert-occurrences' },
  { id: 'payments', name: 'Payment success rate', service: 'Payments', href: '#alert-occurrences' },
  { id: 'search', name: 'Search latency', service: 'Search', href: '#alert-occurrences' },
];
const initial: AlertOccurrenceRow[] = [
  { id: 'checkout-burn', version: '7', policyName: 'Checkout burn', href: '#alert-occurrences', slo: slos[0]!, state: 'firing', severity: 'high', openedAt: '2026-11-12T10:12:00Z', detectedAt: '2026-11-12T10:13:00Z', evidence: { at: '2026-11-12T10:41:00Z', matches: [fast, sustained] }, processing: { state: 'current' } },
  { id: 'payment-burn', version: '3', policyName: 'Payment burn', href: '#alert-occurrences', slo: slos[1]!, state: 'firing', severity: 'medium', openedAt: '2026-11-12T09:54:00Z', detectedAt: '2026-11-12T09:55:00Z', evidence: { at: '2026-11-12T10:41:00Z', matches: [sustained] }, processing: { state: 'current' } },
  { id: 'checkout-slow', version: '4', policyName: 'Checkout sustained burn', href: '#alert-occurrences', slo: slos[0]!, state: 'acknowledged', severity: 'medium', openedAt: '2026-11-12T09:20:00Z', detectedAt: '2026-11-12T09:21:00Z', acknowledged: { actor: 'Jamie Morgan', at: '2026-11-12T09:25:00Z' }, evidence: { at: '2026-11-12T10:41:00Z', matches: [], unavailableReason: 'Missing observations prevent recovery' }, processing: { state: 'current' } },
  { id: 'search-burn', version: '2', policyName: 'Search burn', href: '#alert-occurrences', slo: slos[2]!, state: 'firing', severity: 'low', openedAt: '2026-11-12T10:31:00Z', detectedAt: '2026-11-12T10:32:00Z', evidence: { at: '2026-11-12T10:37:00Z', matches: [fast] }, processing: { state: 'waiting', dueAt: '2026-11-12T10:38:00Z', reason: 'Waiting for calculation' } },
];
const history: AlertOccurrenceRow[] = [
  { ...initial[0]!, id: 'recovered', state: 'resolved', openedAt: '2026-11-12T08:12:00Z', closedAt: '2026-11-12T08:20:00Z', detectedAt: '2026-11-12T08:40:00Z', reconstructed: true, detectedAfterRecovery: true, evidence: { at: '2026-11-12T08:12:00Z', matches: [fast] } },
  { ...initial[1]!, id: 'archived', state: 'disabled', closedAt: '2026-11-12T10:20:00Z', terminalCause: 'SLO archived' },
];
// This controller operates exclusively on fictional catalogue data. The actual list
// consumes server-filtered pages, group counts and persisted command responses.
export function AlertOccurrenceExample() {
  const [paginated, setPaginated] = useState(false);
  const [rows, setRows] = useState(initial), [filters, setFilters] = useState(emptyAlertListFilters), [grouped, setGrouped] = useState(false);
  const [view, setView] = useState<'active' | 'history'>('active'), [viewer, setViewer] = useState(false), [mode, setMode] = useState<'success' | 'pending' | 'conflict'>('success');
  const [action, setAction] = useState<{ id: string; pending: boolean; error?: string }>(), [feedback, setFeedback] = useState('');
  const [size, setSize] = useState(10), [offset, setOffset] = useState(0);
  function changeFilters(next: AlertListFilters) { setFilters(next); setOffset(0); }
  const filtered = (view === 'active' ? rows : history).filter(row =>
    row.policyName.toLowerCase().includes(filters.policyName.toLowerCase()) && (!filters.sloId || row.slo.id === filters.sloId) &&
    (!filters.severity || row.severity === filters.severity) && (!filters.state || row.state === filters.state) &&
    (!filters.minimumAge || (Date.parse(row.closedAt ?? now) - Date.parse(row.openedAt)) / 1000 >= Number(filters.minimumAge)) &&
    (!filters.evidence || (filters.evidence === 'matching' ? row.evidence.matches.length > 0 : filters.evidence === 'unavailable' ? !!row.evidence.unavailableReason : row.processing.state === 'waiting')));
  const ordered = grouped ? [...filtered].sort((a, b) => a.slo.id.localeCompare(b.slo.id)) : filtered;
  const pageOffset = Math.min(offset, Math.max(0, Math.floor((filtered.length - 1) / size) * size));
  const visible = ordered.slice(pageOffset, pageOffset + size);
  const bounds = { total: filtered.length, start: filtered.length ? pageOffset + 1 : 0, end: pageOffset + visible.length, hasPrevious: pageOffset > 0, hasNext: pageOffset + size < filtered.length };
  const page: AlertListPage = grouped ? { ...bounds, kind: 'slo', groups: slos.filter(slo => visible.some(row => row.slo.id === slo.id)).map(slo => ({ slo, total: filtered.filter(row => row.slo.id === slo.id).length, firing: filtered.filter(row => row.slo.id === slo.id && row.state === 'firing').length, items: visible.filter(row => row.slo.id === slo.id) })) } : { ...bounds, kind: 'flat', items: visible };
  function acknowledge(id: string, expectedVersion: string) {
    if (mode === 'pending') { setAction({ id, pending: true }); return; }
    if (mode === 'conflict' || rows.find(row => row.id === id)?.version !== expectedVersion) { setAction({ id, pending: false, error: 'The alert changed. Refresh its current state.' }); return; }
    setRows(current => current.map(row => row.id === id ? { ...row, state: 'acknowledged', version: String(Number(row.version) + 1), acknowledged: { actor: 'Alex Rivera', at: now } } : row));
    setAction(undefined); setFeedback('Acknowledged in this local example only.');
  }
  return <section className="catalogue-section" id="alert-occurrences" aria-labelledby="alert-occurrences-title">
    <div className="section-heading"><p className="eyebrow mono">ALERTS / LIST</p><h2 id="alert-occurrences-title">Alert occurrence list</h2><p>Fictional catalogue data. Actions here do not change Almanac.</p></div>
    <div className="example-row">
      <Button variant="outline" onClick={() => { setView(view === 'active' ? 'history' : 'active'); changeFilters(emptyAlertListFilters()); }}>{view === 'active' ? 'Show history example' : 'Show active example'}</Button>
      <Button variant="outline" aria-pressed={viewer} onClick={() => setViewer(!viewer)}>Viewer example</Button>
      <Button variant="outline" aria-pressed={mode === 'conflict'} onClick={() => { setMode(mode === 'conflict' ? 'success' : 'conflict'); setAction(undefined); }}>Conflict on acknowledgement</Button>
      <Button variant="outline" aria-pressed={mode === 'pending'} onClick={() => { setMode(mode === 'pending' ? 'success' : 'pending'); setAction(undefined); }}>Hold acknowledgement</Button>
      <Button variant="outline" onClick={() => { setPaginated(!paginated); setOffset(0); setAction(undefined); setRows(paginated ? initial : [...initial, ...Array.from({ length: 21 }, (_, index) => ({ ...initial[index % initial.length]!, id: `additional-${index}`, policyName: `Regional burn ${index + 1}` }))]); }}>{paginated ? 'Show compact example' : 'Show paginated example'}</Button>
      <Button variant="ghost" onClick={() => { setRows(initial); setPaginated(false); setAction(undefined); setMode('success'); setViewer(false); setView('active'); setGrouped(false); changeFilters(emptyAlertListFilters()); setFeedback(''); }}>Reset alert list example</Button>
    </div>
    <div className="alert-list-example">
      <AlertOccurrenceList page={page} filters={filters} pageSize={size} view={view} grouped={grouped} now={now} slos={slos.map(slo => ({ id: slo.id, label: slo.name }))} canAcknowledge={!viewer} newPolicyHref="#alert-policy" onFiltersChange={changeFilters} onGroupChange={value => { setGrouped(value); setOffset(0); }} onPageSizeChange={value => { setSize(value); setOffset(0); }} onPrevious={() => setOffset(Math.max(0, pageOffset - size))} onNext={() => setOffset(pageOffset + size)} onRefresh={() => { setAction(undefined); setFeedback('Local example refreshed.'); }} onOpen={() => setFeedback('Opened the local alert example.')} onAcknowledge={acknowledge} acknowledgement={action} gaps={view === 'history' ? [{ id: 'gap', start: '2026-11-10T00:00:00Z', end: '2026-11-11T00:00:00Z', reason: 'Outside automatic catch-up horizon' }] : undefined} />
      <p role="status">{feedback}</p>
    </div>
  </section>;
}
