import { useEffect, useId, useRef, useState } from 'react';
import { Button, PageHeader, StatusMark } from '../components/ui/index.ts';
import { TimelineChartView } from '../components/charts/TimelineChartView.tsx';
import type { TimelineChart } from '../components/charts/timelineModel.ts';
import { pointTime } from '../components/charts/pointInspection.ts';
import { countExact, exactDecimal, percentExact } from '../slos/detailFormat.ts';
import type { AlertLifecycle } from './AlertOccurrenceList.tsx';
import './occurrence-detail.css';

export type AlertEvidenceWindow = {
  label: string; seconds: number; start: string; end: string;
  bad: string | null; total: string | null; observedErrorRate: string | null;
  allowedErrorRate: string; burn: string | null; reason?: string;
};
export type AlertConditionEvidence = {
  id: string; name: string; outcome: 'matching' | 'not_matching' | 'unavailable';
  explanation: string; threshold: string; windows: readonly [AlertEvidenceWindow, AlertEvidenceWindow]; chart: TimelineChart;
};
export type AlertEvidenceSnapshot = {
  evaluatedAt: string; sloTarget: string; sloWindowSeconds: number; policyRevision: string; sloRevision: string; measurementRevision: string;
  sourceGeneration: string; exclusionRevision: string; calculationVersion: string;
  primaryConditionId: string; conditions: AlertConditionEvidence[];
};
export type AlertOccurrenceDetailData = {
  id: string; version: string; policyName: string;
  slo: { id: string; name: string; service: string; href: string };
  state: AlertLifecycle; severity: 'low' | 'medium' | 'high'; reason: string;
  openedAt: string; detectedAt: string; closedAt?: string; terminalCause?: string;
  acknowledged?: { actor: string; at: string };
  processing: { state: 'current' } | { state: 'waiting' | 'behind'; dueAt: string; reason: string; totalDelaySeconds: number; aggregateWaitSeconds?: number };
  original: AlertEvidenceSnapshot; latest: AlertEvidenceSnapshot;
  lifecycle: { id: string; at: string; effectiveAt?: string; title: string; description: string; actor?: string; cause?: string }[];
};
type Props = {
  data: AlertOccurrenceDetailData; now: string; backHref: string; canAcknowledge: boolean;
  onAcknowledge: (id: string, expectedVersion: string) => void; onRefresh?: () => void;
  acknowledgement?: { pending: boolean; error?: string };
};
const states: Record<AlertLifecycle, string> = { firing: 'Firing', acknowledged: 'Acknowledged', resolved: 'Resolved', disabled: 'Disabled', superseded: 'Superseded' };
const outcomes = { matching: 'Matching', not_matching: 'Not matching', unavailable: 'Unavailable' };
function windowDuration(seconds: number) { return seconds % 86400 === 0 ? `${seconds / 86400} d` : seconds % 3600 === 0 ? `${seconds / 3600} h` : seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`; }
function Time({ value }: { value: string }) { return <time dateTime={value} title={value}>{pointTime(value, 'UTC', true)}</time>; }
function age(start: string, end: string) {
  const minutes = Math.max(0, Math.floor((Date.parse(end) - Date.parse(start)) / 60000));
  return minutes < 1 ? '<1 min' : minutes < 60 ? `${minutes} min` : `${Math.floor(minutes / 60)} h ${minutes % 60} min`;
}
function Provenance({ snapshot }: { snapshot: AlertEvidenceSnapshot }) {
  return <dl className="alert-detail-provenance">
    <div><dt>Evaluated</dt><dd><Time value={snapshot.evaluatedAt} /></dd></div>
    <div><dt>SLO target</dt><dd>{percentExact(snapshot.sloTarget)}</dd></div>
    <div><dt>Rolling SLO window</dt><dd>{windowDuration(snapshot.sloWindowSeconds)}</dd></div>
    <div><dt>Policy revision</dt><dd>{snapshot.policyRevision}</dd></div>
    <div><dt>SLO definition revision</dt><dd>{snapshot.sloRevision}</dd></div>
    <div><dt>Measurement revision</dt><dd>{snapshot.measurementRevision}</dd></div>
    <div><dt>Source generation</dt><dd>{snapshot.sourceGeneration}</dd></div>
    <div><dt>Exclusion revision</dt><dd>{snapshot.exclusionRevision}</dd></div>
    <div><dt>Calculation version</dt><dd>{snapshot.calculationVersion}</dd></div>
  </dl>;
}
function Windows({ condition, bounds = false }: { condition: AlertConditionEvidence; bounds?: boolean }) {
  return <div className="alert-detail-windows">{condition.windows.map((window, index) => <div className={`alert-detail-window alert-detail-window--${index}`} key={`${window.label}:${index}`}>
    <span className="alert-detail-window__label">{window.label} · {windowDuration(window.seconds)}</span>
    <strong className={condition.outcome === 'matching' && window.burn !== null ? 'is-matching' : undefined}>{window.burn === null ? 'Unavailable' : `${exactDecimal(window.burn)}×`}</strong>
    <span>Threshold ≥ {exactDecimal(condition.threshold)}×</span>
    <small>{window.bad !== null && window.total !== null ? `${countExact(window.bad)} / ${countExact(window.total)} unsuccessful events` : window.reason ?? 'Event counts unavailable'}</small>
    {bounds && <small>Observed error {percentExact(window.observedErrorRate)} · Allowed error {percentExact(window.allowedErrorRate)}</small>}
    {bounds && <small className="alert-detail-bounds">[<Time value={window.start} />, <Time value={window.end} />)</small>}
  </div>)}</div>;
}
function Calculation({ condition, snapshot }: { condition: AlertConditionEvidence; snapshot: AlertEvidenceSnapshot }) {
  return <>
    <p>A {percentExact(snapshot.sloTarget)} target allows {percentExact(condition.windows[0].allowedErrorRate)} unsuccessful events.</p>
    <p>Burn compares the observed unsuccessful-event rate with the SLO’s allowed error rate. A burn of 1× uses the allowance at its intended pace.</p>
    <div className="alert-detail-calculations">{condition.windows.map((window, index) => <section key={index}>
      <h3>{window.label} · {windowDuration(window.seconds)}</h3>
      <dl><div><dt>Unsuccessful events</dt><dd>{window.bad !== null && window.total !== null ? `${countExact(window.bad)} bad of ${countExact(window.total)}` : 'Unavailable'}</dd></div>
        <div><dt>Observed error rate</dt><dd>{percentExact(window.observedErrorRate)}</dd></div>
        <div><dt>Allowed error rate</dt><dd>{percentExact(window.allowedErrorRate)}</dd></div></dl>
      <div className="alert-detail-equation">{window.observedErrorRate === null || window.burn === null ? <p>{window.reason ?? 'Calculation unavailable'}</p> : <><span>{percentExact(window.observedErrorRate)} ÷ {percentExact(window.allowedErrorRate)}</span><strong>{exactDecimal(window.burn)}× burn</strong></>}</div>
      <small className="alert-detail-bounds">Window [<Time value={window.start} />, <Time value={window.end} />)</small>
    </section>)}</div>
    <p>Both windows must meet the {exactDecimal(condition.threshold)}× threshold in the same evaluation for this condition to match.</p>
    <p className="alert-detail-calculation-context">Rolling SLO window: {windowDuration(snapshot.sloWindowSeconds)} · SLO definition revision {snapshot.sloRevision}</p>
    <p className="alert-detail-condition-outcome"><strong>{outcomes[condition.outcome]}</strong> · {condition.explanation}</p>
  </>;
}

// Supplied snapshots own condition truth, provenance and lifecycle. This view never
// recomputes burn, infers recovery, or acknowledges before its owner supplies a result.
export function AlertOccurrenceDetail({ data, now, backHref, canAcknowledge, onAcknowledge, onRefresh, acknowledgement }: Props) {
  const id = useId(), heading = useRef<HTMLHeadingElement>(null), error = useRef<HTMLDivElement>(null);
  const requested = useRef<HTMLButtonElement | null>(null);
  const lastError = useRef<string | undefined>(undefined);
  const [selectedId, setSelectedId] = useState(data.latest.primaryConditionId);
  useEffect(() => { setSelectedId(data.latest.primaryConditionId); }, [data.id, data.latest.primaryConditionId]);
  const selected = data.latest.conditions.find(condition => condition.id === selectedId) ?? data.latest.conditions.find(condition => condition.id === data.latest.primaryConditionId);
  useEffect(() => {
    const changedError = acknowledgement?.error !== lastError.current;
    lastError.current = acknowledgement?.error;
    if (acknowledgement?.pending) return;
    if (acknowledgement?.error) {
      if (requested.current || changedError) error.current?.focus();
      requested.current = null; return;
    }
    if (!requested.current || data.state === 'firing' && canAcknowledge) return;
    if (document.activeElement === document.body || document.activeElement === requested.current) heading.current?.focus();
    requested.current = null;
  }, [data.id, data.state, canAcknowledge, acknowledgement]);
  const tone = data.state === 'firing' ? 'breached' : data.state === 'acknowledged' ? 'stale' : data.state === 'resolved' ? 'healthy' : 'none';
  return <article className="alert-occurrence-detail">
    <a href={backHref} data-app-route className="alert-detail-back">← Alerts</a>
    <PageHeader title={data.policyName} headingRef={heading} summary={<><a href={data.slo.href} data-app-route>{data.slo.name}</a><span className="alert-detail-severity">{data.severity[0]!.toUpperCase() + data.severity.slice(1)} severity</span></>}
      actions={data.state === 'firing' && canAcknowledge ? <Button pending={acknowledgement?.pending} onClick={event => { requested.current = event.currentTarget; onAcknowledge(data.id, data.version); }}>Acknowledge</Button> : undefined} />
    {acknowledgement?.error && <div ref={error} className="alert-detail-error" role="alert" tabIndex={-1}><p>{acknowledgement.error}</p>{onRefresh && <Button variant="outline" onClick={() => { heading.current?.focus(); onRefresh(); }}>Refresh alert</Button>}</div>}
    <section className={`alert-detail-reason alert-detail-reason--${tone}`} aria-label="Current alert state">
      <div><StatusMark tone={tone} label={states[data.state]} /><p>{data.reason}</p></div>
      <p className="alert-detail-time">Started <Time value={data.openedAt} /> · {data.closedAt ? 'Duration' : 'Open'} {age(data.openedAt, data.closedAt ?? now)}</p>
      {data.acknowledged && <p>Acknowledged by <strong>{data.acknowledged.actor}</strong> · <Time value={data.acknowledged.at} /></p>}
      {data.closedAt && <p>Closed <Time value={data.closedAt} /></p>}{data.terminalCause && <p>{data.terminalCause}</p>}
    </section>
    {data.processing.state !== 'current' && <aside className="alert-detail-processing" aria-label="Calculation progress"><strong>{data.processing.reason}</strong><p>Evaluation due <Time value={data.processing.dueAt} /> · Last evaluated <Time value={data.latest.evaluatedAt} /></p><p>Total delay: {data.processing.totalDelaySeconds} seconds{data.processing.aggregateWaitSeconds !== undefined && <> · Aggregate wait: {data.processing.aggregateWaitSeconds} seconds</>}</p><p>The committed alert state is shown above.</p></aside>}
    <section className="alert-detail-card alert-detail-evidence" aria-labelledby={`${id}-evidence`}>
      <h2 id={`${id}-evidence`} className="sr-only">Alert evidence</h2>
      {data.latest.conditions.length > 1 && <div className="alert-detail-conditions" role="group" aria-label="Inspect condition evidence">{data.latest.conditions.map(condition => <Button key={condition.id} variant={condition.id === selected?.id ? 'selected' : 'outline'} aria-pressed={condition.id === selected?.id} onClick={() => setSelectedId(condition.id)}>{condition.name}<small>{outcomes[condition.outcome]}</small></Button>)}</div>}
      {selected ? <>
        <Windows condition={selected} />
        <div className="alert-detail-chart-title"><h2>Burn rate</h2><Time value={data.latest.evaluatedAt} /></div>
        <TimelineChartView chart={selected.chart} showHeader={false} generation={`${data.version}:${data.latest.evaluatedAt}:${selected.id}`} inspection />
        <div className="alert-detail-legend" aria-label="Chart series">{selected.chart.lanes.flatMap(lane => lane.series).map((series, index) => <span key={series.id}><i className={`alert-detail-series--${series.color === 'critical' ? 'critical' : series.color === 'primary' ? 0 : index}`} aria-hidden="true" />{series.label}</span>)}</div>
        <p className="alert-detail-condition-outcome">{data.latest.conditions.length === 1 && <strong>{outcomes[selected.outcome]} · </strong>}{selected.explanation}</p>
      </> : <p role="alert">Latest condition evidence unavailable.</p>}
      <details className="alert-detail-disclosure"><summary>Latest evidence provenance</summary><Provenance snapshot={data.latest} /></details>
      <details className="alert-detail-disclosure"><summary>Original firing evidence</summary><section aria-label="Original firing evidence">{data.original.conditions.map(condition => <div className="alert-detail-original-condition" key={condition.id}><h3>{condition.name} · {outcomes[condition.outcome]}</h3><Windows condition={condition} bounds /><p>{condition.explanation}</p></div>)}<Provenance snapshot={data.original} /></section></details>
    </section>
    <details className="alert-detail-card alert-detail-expand"><summary>How this is calculated</summary><div>{selected ? <Calculation condition={selected} snapshot={data.latest} /> : <p>Calculation evidence unavailable.</p>}<p className="alert-detail-calculation-time">Evaluated <Time value={data.latest.evaluatedAt} /></p></div></details>
    <details className="alert-detail-card alert-detail-expand" open><summary>Lifecycle history</summary><p className="alert-detail-detection">Detected <Time value={data.detectedAt} /></p><ol className="alert-detail-history">{data.lifecycle.map(event => <li key={event.id}><Time value={event.at} /><div><h3>{event.title}</h3><p>{event.description}</p>{event.actor && <p>{event.actor}</p>}{event.effectiveAt && event.effectiveAt !== event.at && <p>Effective <Time value={event.effectiveAt} /></p>}{event.cause && <p>{event.cause}</p>}</div></li>)}</ol></details>
  </article>;
}
