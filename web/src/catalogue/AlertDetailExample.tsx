import { useState } from 'react';
import { AlertOccurrenceDetail, type AlertConditionEvidence, type AlertEvidenceSnapshot, type AlertOccurrenceDetailData } from '../alerts/AlertOccurrenceDetail.tsx';
import { Button } from '../components/ui/index.ts';

const now = '2026-11-12T10:42:00Z';
function condition(at: string, original = false): AlertConditionEvidence {
  const end = Date.parse(at), recent = original ? '6.8' : '12.5', sustained = original ? '6.2' : '7.4';
  const values = original ? ['2.8', '3.2', '4.1', '4.8', '5.2', '6.1', '6.8'] : ['4.2', '4.8', '5.2', '6.8', '8.4', '10.2', '12.5'];
  const long = original ? ['3.4', '3.8', '4.2', '4.8', '5.1', '5.8', '6.2'] : ['5.1', '5.4', '5.8', '6.2', '6.6', '7', '7.4'];
  const points = (samples: string[]) => samples.map((value, index) => ({ timestamp: new Date(end - (6 - index) * 600000).toISOString(), value, coverage: 'complete' as const }));
  return {
    id: 'sustained', name: 'Sustained burn', outcome: 'matching', explanation: 'Both burn windows meet the threshold.', threshold: '6',
    windows: [
      { label: 'Recent window', seconds: 1800, start: new Date(end - 1800000).toISOString(), end: at, bad: original ? '204' : '375', total: '30000', observedErrorRate: original ? '0.0068' : '0.0125', allowedErrorRate: '0.001', burn: recent },
      { label: 'Sustained window', seconds: 21600, start: new Date(end - 21600000).toISOString(), end: at, bad: original ? '2232' : '2664', total: '360000', observedErrorRate: original ? '0.0062' : '0.0074', allowedErrorRate: '0.001', burn: sustained },
    ],
    chart: { title: 'Burn rate', summary: 'Supplied 30-minute and 6-hour burn evidence, with a 6× threshold.', start: new Date(end - 3600000).toISOString(), end: at, stepMs: 600000, fillExpectedGaps: false, showLaneNames: false,
      lanes: [{ id: 'burn', label: 'Burn', unit: '×', series: [{ id: 'recent', label: '30 min burn', color: 'critical', points: points(values) }, { id: 'sustained', label: '6 h burn', color: 'primary', points: points(long) }] }], referenceLines: [{ lane: 'burn', value: '6', label: '6× threshold' }] },
  };
}
function snapshot(at: string, original = false): AlertEvidenceSnapshot {
  return { evaluatedAt: at, sloTarget: '0.999', sloWindowSeconds: 2592000, policyRevision: '7', sloRevision: '3', measurementRevision: '2', sourceGeneration: original ? '481' : '491', exclusionRevision: '4', calculationVersion: 'occurrences-v1', primaryConditionId: 'sustained', conditions: [condition(at, original)] };
}
const initial: AlertOccurrenceDetailData = {
  id: 'checkout-sustained', version: '11', policyName: 'Checkout sustained burn',
  slo: { id: 'checkout-availability', name: 'Checkout availability', service: 'Checkout', href: '/design-system#alert-detail-slo' },
  state: 'firing', severity: 'medium', reason: 'Both burn windows exceed the threshold.', openedAt: '2026-11-12T10:32:00Z', detectedAt: '2026-11-12T10:32:08Z', processing: { state: 'current' },
  original: snapshot('2026-11-12T10:32:00Z', true), latest: snapshot(now),
  lifecycle: [{ id: 'fired', at: '2026-11-12T10:32:08Z', effectiveAt: '2026-11-12T10:32:00Z', title: 'Alert fired', description: 'Both burn windows exceeded the threshold in the same evaluation.' }],
};
function handoff(unavailable = false): AlertOccurrenceDetailData {
  const latest = condition(now);
  const fast: AlertConditionEvidence = { ...latest, id: 'fast', name: 'Fast burn', outcome: 'not_matching', explanation: 'The fast-burn condition is no longer matching.', threshold: '14.4',
    windows: [{ ...latest.windows[0], seconds: 300, start: '2026-11-12T10:37:00Z', total: '5000', bad: '30', observedErrorRate: '0.006', burn: '6' }, { ...latest.windows[1], seconds: 3600, start: '2026-11-12T09:42:00Z', total: '60000', bad: '480', observedErrorRate: '0.008', burn: '8' }],
    chart: { ...latest.chart, summary: 'The supplied fast-burn condition is not matching.', referenceLines: [{ lane: 'burn', value: '14.4', label: '14.4× threshold' }], lanes: [{ id: 'burn', label: 'Burn', unit: '×', series: [{ id: 'short', label: '5 min burn', color: 'critical', points: [{ timestamp: now, value: '6', coverage: 'complete' }] }, { id: 'long', label: '1 h burn', color: 'primary', points: [{ timestamp: now, value: '8', coverage: 'complete' }] }] }] } };
  const slow: AlertConditionEvidence = unavailable ? { ...latest, outcome: 'unavailable', explanation: 'Missing observations prevent recovery.', windows: [{ ...latest.windows[0], bad: null, total: null, observedErrorRate: null, burn: null, reason: 'Missing observations' }, { ...latest.windows[1], bad: null, total: null, observedErrorRate: null, burn: null, reason: 'Missing observations' }], chart: { ...latest.chart, lanes: latest.chart.lanes.map(lane => ({ ...lane, series: lane.series.map(series => ({ ...series, points: series.points.map((point, index) => index < 5 ? point : { ...point, value: null, coverage: 'missing', reason: 'Missing observations' }) })) })) } } : latest;
  return { ...initial, state: 'acknowledged', acknowledged: { actor: 'Jamie Morgan', at: '2026-11-12T10:34:00Z' }, reason: unavailable ? 'Acknowledged; recovery is not confirmed.' : 'The sustained-burn condition still matches.', latest: { ...initial.latest, primaryConditionId: 'sustained', conditions: [fast, slow] }, lifecycle: [...initial.lifecycle, { id: 'ack', at: '2026-11-12T10:34:00Z', title: 'Alert acknowledged', description: 'Acknowledged globally for this occurrence.', actor: 'Jamie Morgan' }] };
}

function clearSnapshot(): AlertEvidenceSnapshot {
  const supplied = snapshot(now), condition = supplied.conditions[0]!;
  return { ...supplied, sourceGeneration: '492', conditions: [{ ...condition, outcome: 'not_matching', explanation: 'Both burn windows are below the threshold.',
    windows: [{ ...condition.windows[0], bad: '30', observedErrorRate: '0.001', burn: '1' }, { ...condition.windows[1], bad: '720', observedErrorRate: '0.002', burn: '2' }],
    chart: { ...condition.chart, summary: 'The latest supplied evaluation confirms both windows are below the threshold.', lanes: condition.chart.lanes.map(lane => ({ ...lane, series: lane.series.map((series, index) => ({ ...series, points: series.points.map((point, pointIndex) => pointIndex === series.points.length - 1 ? { ...point, value: index === 0 ? '1' : '2' } : point) })) })) },
  }] };
}

// These callbacks simulate committed responses only in the catalogue. No API or
// production route exists, and no management authority is inferred here.
export function AlertDetailExample() {
  const [data, setData] = useState(initial), [viewer, setViewer] = useState(false);
  const [mode, setMode] = useState<'success' | 'pending' | 'error' | 'resolved'>('success');
  const [action, setAction] = useState<{ pending: boolean; error?: string }>();
  function show(next: AlertOccurrenceDetailData) { setData(next); setAction(undefined); }
  function acknowledge(id: string, version: string) {
    if (mode === 'pending') { setAction({ pending: true }); return; }
    if (mode === 'error' || id !== data.id || version !== data.version) { setAction({ pending: false, error: 'The alert changed. Refresh to inspect the committed state.' }); return; }
    if (mode === 'resolved') { show({ ...data, version: '12', state: 'resolved', latest: clearSnapshot(), closedAt: now, reason: 'All conditions are not matching.', lifecycle: [...data.lifecycle, { id: 'resolved', at: now, title: 'Alert resolved', description: 'All conditions were confirmed not matching before acknowledgement.' }] }); return; }
    show({ ...data, version: '12', state: 'acknowledged', acknowledged: { actor: 'Alex Rivera', at: now }, lifecycle: [...data.lifecycle, { id: 'acknowledged', at: now, title: 'Alert acknowledged', description: 'Acknowledged globally for this occurrence.', actor: 'Alex Rivera' }] });
  }
  return <section className="catalogue-section" id="alert-detail" aria-labelledby="alert-detail-title">
    <div className="section-heading"><p className="eyebrow mono">ALERTS / DETAIL</p><h2 id="alert-detail-title">Alert occurrence detail</h2><p>Fictional supplied evidence. Actions simulate committed responses locally and do not change Almanac.</p></div>
    <div className="example-row">
      <Button variant="outline" onClick={() => show(handoff())}>Show condition handoff</Button>
      <Button variant="outline" onClick={() => show(handoff(true))}>Show unavailable evidence</Button>
      <Button variant="outline" onClick={() => show({ ...initial, latest: snapshot('2026-11-12T10:37:00Z'), processing: { state: 'waiting', dueAt: '2026-11-12T10:38:00Z', reason: 'Waiting for aggregate preparation', totalDelaySeconds: 240, aggregateWaitSeconds: 180 } })}>Show calculation waiting</Button>
      <Button variant="outline" onClick={() => show({ ...initial, state: 'disabled', closedAt: now, terminalCause: 'SLO archived', reason: 'Disabled because its SLO was archived.', lifecycle: [...initial.lifecycle, { id: 'archived', at: now, title: 'Alert disabled', description: 'The SLO was archived.', cause: 'Administrative closure; recovery was not inferred.' }] })}>Show archived occurrence</Button>
      <Button variant="outline" aria-pressed={viewer} onClick={() => setViewer(!viewer)}>Detail viewer example</Button>
      <Button variant="outline" onClick={() => show({ ...initial, policyName: 'Checkout availability across all regional gateways and external payment providers', slo: { ...initial.slo, name: 'Checkout availability including regional gateway retries and external payment provider responses' } })}>Show long detail names</Button>
      <Button variant="outline" aria-pressed={mode === 'error'} onClick={() => { setMode('error'); setAction(undefined); }}>Fail acknowledgement</Button>
      <Button variant="outline" aria-pressed={mode === 'pending'} onClick={() => { setMode('pending'); setAction(undefined); }}>Hold acknowledgement</Button>
      <Button variant="outline" aria-pressed={mode === 'resolved'} onClick={() => { setMode('resolved'); setAction(undefined); }}>Resolve on acknowledgement</Button>
      <Button variant="ghost" onClick={() => { show(initial); setMode('success'); setViewer(false); }}>Reset alert detail</Button>
    </div>
    <AlertOccurrenceDetail data={data} now={now} backHref="/design-system#alert-occurrences" canAcknowledge={!viewer} onAcknowledge={acknowledge} acknowledgement={action} onRefresh={() => setAction(undefined)} />
    <section id="alert-detail-slo" className="alert-detail-linked-example"><h3>Linked fictional SLO</h3><p>Checkout availability. This catalogue destination demonstrates the supplied SLO link; production navigation awaits the alert API.</p></section>
  </section>;
}
