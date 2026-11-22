import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { DatePicker } from '../components/ui/DatePicker.tsx';
import { Dialog } from '../components/ui/Dialog.tsx';
import { Button } from '../components/ui/Button.tsx';
import { SelectField } from '../components/ui/SelectField.tsx';
import { Switch } from '../components/ui/Switch.tsx';
import { TextField } from '../components/ui/TextField.tsx';
import { createSliClient, SliFailure, type Sli, type SliBackfillAdmission, type SliBackfillJob, type SliBackfillPlan } from './client.ts';

const client = createSliClient();
const rangeFormat = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric',
  hour: '2-digit', minute: '2-digit', timeZone: 'UTC' });
const secondsRangeFormat = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric',
  hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'UTC' });

function utcRange(start: string, end: string) {
  const from = new Date(start), to = new Date(end);
  const format = from.getUTCSeconds() || to.getUTCSeconds() ? secondsRangeFormat : rangeFormat;
  return `${format.formatRange(from, to)} UTC`;
}
const presets = [
  { value: '1', label: 'Last 1 day' },
  { value: '7', label: 'Last 7 days' },
  { value: '14', label: 'Last 14 days' },
  { value: '30', label: 'Last 30 days' },
];

function rangeFor(sli: Sli, preset: string) {
  const cadence = sli.polling_interval_seconds * 1_000;
  const end = Math.floor((Date.now() - (sli.settling_delay_seconds + 120) * 1_000) / cadence) * cadence;
  const requested = end - Number(preset) * 86_400_000;
  const start = Math.ceil(requested / cadence) * cadence;
  return { start: new Date(start).toISOString(), end: new Date(end).toISOString() };
}

function utc(value: string) {
  return new Date(value).toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC');
}

const states: Record<SliBackfillJob['state'], string> = {
  queued: 'Queued', running: 'Running', completed: 'Completed', 'completed-with-gaps': 'Completed with gaps', cancelled: 'Cancelled',
};

const gapStates = { eligible: 'Eligible', automatic: 'Scheduled by automatic repair', manual: 'Queued in a manual job', 'outside-retention': 'Outside recovery window', 'previous-failure': 'Previous attempt failed' };
function elapsed(seconds: number) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  return [hours && `${hours}h`, minutes && `${minutes}m`, seconds % 60 && `${seconds % 60}s`].filter(Boolean).join(' ') || '0m';
}


function RangeEndpoint({ label, value, today, disabled, onChange }: {
  label: 'Start' | 'End'; value: string; today: string; disabled: boolean; onChange: (value: string) => void;
}) {
  const [day = '', clock = ''] = value.split('T');
  return <div className="sli-backfill-endpoint">
    <DatePicker label={`${label} date`} value={day} today={today} disabled={disabled} onValueChange={date => onChange(`${date}T${clock}`)} />
    <TextField id={`sli-backfill-${label.toLowerCase()}-time`} label={`${label} time (UTC)`} value={clock}
      required disabled={disabled} pattern="([01][0-9]|2[0-3]):[0-5][0-9](:[0-5][0-9])?" placeholder="HH:MM"
      maxLength={8} autoComplete="off" onChange={event => onChange(`${day}T${event.target.value}`)} />
  </div>;
}

export function SliBackfills({ sli, datasourceName, context, canWrite, onMeasurementsChanged }: { sli: Sli; datasourceName: string; context: AuthContext; canWrite: boolean; onMeasurementsChanged: (earlierFrom?: string) => void }) {
  const [open, setOpen] = useState(false);
  const [preset, setPreset] = useState('30');
  const [dialog, setDialog] = useState<'preset' | 'custom' | null>(null);
  const trigger = useRef<HTMLElement>(null);
  const [history, setHistory] = useState<SliBackfillPlan | null>(null);
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  const [retryExhausted, setRetryExhausted] = useState(false);
  const [includeEarlierHistory, setIncludeEarlierHistory] = useState(false);
  const [preview, setPreview] = useState<SliBackfillAdmission | null>(null);
  const rangeFields = useRef<HTMLDivElement>(null);
  const today = new Date().toISOString().slice(0, 10);
  const eligibleGaps = preview?.plan?.gaps.filter(gap => gap.state === 'eligible') ?? [];
  const selectedStart = dialog === 'custom' ? Date.parse(`${start}Z`) : Date.parse(preview?.start ?? rangeFor(sli, preset).start);
  const startsBeforeHistory = selectedStart < Date.parse(sli.effective_from);
  const canCollectEarlier = sli.measurement_revision === '1' && sli.activated_at !== null;
  const [jobs, setJobs] = useState<SliBackfillJob[]>([]);
  const [working, setWorking] = useState('');
  const [error, setError] = useState('');
  const [loadError, setLoadError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const requestKey = useRef('');
  const uncertainRequest = useRef<{ selection: string; key: string } | null>(null);
  const pendingRequest = useRef<AbortController | null>(null);
  const lastRecovered = useRef('');
  useEffect(() => () => pendingRequest.current?.abort(), []);

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let lastProgress: string | undefined;
    let lastCoverage = 0;
    async function refresh() {
      try {
        const jobs = await client.backfills(sli.id, controller.signal);
        if (controller.signal.aborted) return;
        setJobs(jobs);
        const progress = JSON.stringify(jobs.map(job => [job.id, job.state, job.committed, job.recovered_intervals]));
        const settled = jobs.every(job => job.state !== 'queued' && job.state !== 'running');
        if (lastProgress === undefined || Date.now() - lastCoverage >= 30_000 || settled && progress !== lastProgress) {
          const recovered = jobs.filter(job => (job.recovered_intervals ?? 0) > 0)
            .map(job => `${job.id}:${job.recovered_intervals}`).join(',');
          if (recovered !== lastRecovered.current) {
            lastRecovered.current = recovered;
            if (recovered) onMeasurementsChanged();
          }
          const range = rangeFor(sli, preset);
          const coverage = await client.requestBackfill(sli.id, { start: range.start, end: range.end, preview: true }, context, crypto.randomUUID(), controller.signal);
          if (controller.signal.aborted) return;
          setHistory(coverage.plan ?? null);
          lastProgress = progress;
          lastCoverage = Date.now();
        }
        setLoaded(true);
        setLoadError('');
      } catch {
        if (!controller.signal.aborted) setLoadError('Could not refresh gap history or backfill jobs.');
      }
      if (!controller.signal.aborted) timer = setTimeout(refresh, 3_000);
    }
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [open, preset, sli, context, onMeasurementsChanged]);

  function edit() {
    setPreview(null);
    setError('');
  }

  function closeDialog() {
    pendingRequest.current?.abort();
    pendingRequest.current = null;
    setWorking('');
    setDialog(null);
    edit();
    if (working === 'confirm') setError('The request may have reached Almanac. Check Backfill jobs for its status.');
  }

  async function requestRange(confirmed: boolean, includeEarlier = includeEarlierHistory) {
    if (pendingRequest.current) return;
    const controller = new AbortController();
    pendingRequest.current = controller;
    setError('');
    setWorking(confirmed ? 'confirm' : 'preview');
    try {
      const selection = confirmed && preview ? { start: preview.start, end: preview.end }
        : dialog === 'custom' ? { start: new Date(`${start}Z`).toISOString(), end: new Date(`${end}Z`).toISOString() }
          : rangeFor(sli, preset);
      const retry = dialog === 'custom' && retryExhausted;
      const earlier = includeEarlier && Date.parse(selection.start) < Date.parse(sli.effective_from);
      const earlierContext = confirmed && earlier ? preview?.earlier_history_context : undefined;
      const selected = JSON.stringify([selection.start, selection.end, retry, earlier]);
      if (!confirmed) {
        requestKey.current = uncertainRequest.current?.selection === selected ? uncertainRequest.current.key : crypto.randomUUID();
      } else {
        uncertainRequest.current = { selection: selected, key: requestKey.current };
      }
      const key = confirmed ? requestKey.current : crypto.randomUUID();
      const result = await client.requestBackfill(sli.id, { ...selection, preview: !confirmed, retry_exhausted: retry, include_earlier_history: earlier || undefined, earlier_history_context: earlierContext }, context, key, controller.signal);
      if (controller.signal.aborted) return;
      if (confirmed) {
        uncertainRequest.current = null;
        if (result.earlier_history_from) onMeasurementsChanged(result.earlier_history_from);
        setPreview(null);
        setDialog(null);
      } else {
        setPreview(result);
      }
    } catch (failure) {
      if (controller.signal.aborted) return;
      if (failure instanceof SliFailure && failure.code === 'backfill_earlier_changed') {
        uncertainRequest.current = null;
        setPreview(null);
      }
      setError(failure instanceof RangeError ? 'Enter both UTC dates.' : failure instanceof Error ? failure.message : 'Could not request historical collection.');
    } finally {
      if (pendingRequest.current === controller) {
        pendingRequest.current = null;
        setWorking('');
      }
    }
  }

  async function cancel(jobID: string) {
    setError('');
    setWorking(jobID);
    try {
      await client.cancelBackfill(sli.id, jobID, context);
      setJobs(await client.backfills(sli.id));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not cancel the job.');
    } finally {
      setWorking('');
    }
  }

  return <details id="sli-backfills" className="sli-detail-disclosure" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Historical backfill<span>{history ? `${history.gap_count} gaps · ${elapsed(history.missing_intervals * history.cadence_seconds)} missing` : 'Gap history and recovery'}</span></summary>
    <div className="sli-backfill-body">
      <div className="sli-backfill-toolbar">
        <SelectField id="sli-backfill-range" label="Gap history" value={preset} options={presets} onChange={event => setPreset(event.target.value)} />
        {canWrite && <div className="sli-actions">
          <Button variant="outline" onClick={event => { trigger.current = event.currentTarget; edit(); setRetryExhausted(false); setIncludeEarlierHistory(false); setDialog('custom'); }}>Custom range</Button>
          <Button disabled={history?.eligible_intervals === 0 && !startsBeforeHistory} onClick={event => {
            trigger.current = event.currentTarget;
            edit();
            setIncludeEarlierHistory(false);
            setDialog('preset');
            void requestRange(false, false);
          }}>{startsBeforeHistory ? 'Review backfill' : 'Review recoverable gaps'}</Button>
        </div>}
      </div>
      {dialog === null && error && <p role="alert" className="sli-error">{error}</p>}
      {loadError && <p className="sli-error" role="alert">{loadError}</p>}
      {history && <p className="sli-muted">{history.datasource_retention_days ? `Datasource retention is ${history.datasource_retention_days} days.` : 'Datasource retention is unknown. Recovery is best effort.'} Almanac requests at most 30 days. Gap repair starts at {utc(history.recovery_start)}.</p>}
      {loaded && jobs.length === 0 && <p className="sli-muted">No backfill jobs yet.</p>}
      {jobs.length > 0 && <ul className="sli-backfill-jobs" aria-label="Backfill jobs">
        {jobs.map(job => <li key={job.id}>
          <div><strong>{states[job.state]}</strong>{job.automatic && <span> · Automatic repair</span>}<p>{utc(job.start)} to {utc(job.end)}</p>
            <p>{job.recovered_intervals ?? 0} / {job.requested_intervals ?? 0} intervals recovered · {job.committed} / {job.chunks} chunks committed{job.gaps > 0 && ` · ${job.gaps} with gaps`}</p>
            {job.canonical_conflict && <p>Historical counts differed. Almanac kept the existing observations.</p>}</div>
          {canWrite && ['queued', 'running'].includes(job.state) && <Button variant="outline" disabled={!!working} pending={working === job.id} onClick={() => void cancel(job.id)}>Cancel job</Button>}
        </li>)}
      </ul>}
      {history && <>
        {history.gaps.length ? <div className="sli-detail-table-wrap" role="region" aria-label="Historical gaps" tabIndex={0}>
          <table className="sli-intervals-table"><thead><tr><th scope="col">Gap (UTC)</th><th scope="col">Missing data</th><th scope="col">Recovery</th></tr></thead>
            <tbody>{history.gaps.map(gap => <tr key={gap.start}><td>{utc(gap.start)} – {utc(gap.end)}</td><td>{elapsed(gap.intervals * history.cadence_seconds)} · {gap.intervals} intervals</td><td>{gapStates[gap.state]}</td></tr>)}</tbody>
          </table>
        </div> : <p>No settled intervals are missing in this period.</p>}
        {history.gap_count > history.gaps.length && <p className="sli-muted">Showing the newest {history.gaps.length} of {history.gap_count} gaps. Choose a shorter range to inspect older gaps.</p>}
      </>}
    </div>
    <Dialog open={dialog !== null} onClose={closeDialog} title={dialog === 'custom' ? 'Custom backfill range' : 'Review backfill'} triggerRef={trigger}>
      <form className="sli-backfill-form" onSubmit={event => { event.preventDefault(); void requestRange(false); }}>
        <div className="sli-backfill-fields">
          {dialog === 'custom' && !preview && <div className="sli-backfill-dates" ref={rangeFields}>
            <RangeEndpoint label="Start" value={start} today={today} disabled={!!working}
              onChange={value => { setStart(value); setIncludeEarlierHistory(false); edit(); }} />
            <RangeEndpoint label="End" value={end} today={today} disabled={!!working}
              onChange={value => { setEnd(value); setIncludeEarlierHistory(false); edit(); }} />
            {context.session?.role === 'administrator' &&
              <Switch label="Include previously failed intervals" checked={retryExhausted} disabled={!!working}
                onChange={event => { setRetryExhausted(event.target.checked); edit(); }} />}
          </div>}
          {startsBeforeHistory && (!preview || dialog === 'preset' && !preview.earlier_history_from) && <div>
            {canCollectEarlier ? <Switch label="Include earlier history" checked={includeEarlierHistory} disabled={!!working}
              hint={`Measurement history starts ${utc(sli.effective_from)}.`}
              onChange={event => { setIncludeEarlierHistory(event.target.checked); edit(); }} />
              : <p>Earlier history requires an active original measurement definition.</p>}
          </div>}
          {preview && <div role="status">
            {(!preview.earlier_history_until || Date.parse(preview.earlier_history_until) < Date.parse(preview.end)) &&
              <p>{utcRange(preview.start, preview.end)}</p>}
            {preview.earlier_history_from && preview.earlier_history_until && <>
              <p><strong>Earlier history</strong><br />{utcRange(preview.earlier_history_from, preview.earlier_history_until)}</p>
              <details className="sli-backfill-query">
                <summary>Original queries · {datasourceName}</summary>
                <dl>
                  <div><dt>Good events</dt><dd><code>{sli.good_query}</code></dd></div>
                  <div><dt>Total events</dt><dd><code>{sli.total_query}</code></dd></div>
                </dl>
              </details>
              {preview.plan?.datasource_retention_days === 0 && <p>Datasource retention is unknown.</p>}
            </>}
            {preview.plan && <dl className="sli-backfill-preview-facts">
              <div><dt>Intervals to recover</dt><dd>{preview.plan.eligible_intervals}</dd></div>
              {([
                ['Already covered', preview.plan.covered_intervals],
                ['Scheduled by automatic repair', preview.plan.automatic_intervals],
                ['Queued in manual jobs', preview.plan.manual_intervals],
                ['Outside recovery window', preview.plan.outside_retention_intervals],
                ['Previous attempt failed', preview.plan.previous_failure_intervals],
              ] as const).filter(([, count]) => count > 0).map(([label, count]) =>
                <div key={label}><dt>{label}</dt><dd>{count}</dd></div>)}
            </dl>}
            {eligibleGaps.length > 0 && (eligibleGaps.length > 1 || !preview.earlier_history_from &&
              (eligibleGaps[0]!.start !== preview.start || eligibleGaps[0]!.end !== preview.end)) && <details>
              <summary>Recoverable ranges</summary>
              <ul>{eligibleGaps.map(gap => <li key={gap.start}>{utcRange(gap.start, gap.end)} · {gap.intervals}</li>)}</ul>
            </details>}
          </div>}
          {working === 'preview' && <p role="status">Checking which intervals can be recovered…</p>}
          {error && <p className="sli-error" role="alert">{error}</p>}
        </div>
        <div className="sli-actions">
          {preview && dialog === 'custom' && <Button variant="outline" disabled={!!working} onClick={() => {
            edit();
            requestAnimationFrame(() => rangeFields.current?.querySelector('button')?.focus());
          }}>Edit range</Button>}
          {!preview
            ? <Button type="submit" pending={working === 'preview'}>Preview range</Button>
            : <Button disabled={preview.chunk_count === 0 || !!working} pending={working === 'confirm'} onClick={() => void requestRange(true)}>Queue backfill</Button>}
        </div>
      </form>
    </Dialog>
  </details>;
}
