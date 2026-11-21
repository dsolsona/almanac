import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Dialog } from '../components/ui/Dialog.tsx';
import { Button } from '../components/ui/Button.tsx';
import { SelectField } from '../components/ui/SelectField.tsx';
import { Switch } from '../components/ui/Switch.tsx';
import { TextField } from '../components/ui/TextField.tsx';
import { createSliClient, type Sli, type SliBackfillAdmission, type SliBackfillJob, type SliBackfillPlan } from './client.ts';

const client = createSliClient();
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

export function SliBackfills({ sli, context, canWrite }: { sli: Sli; context: AuthContext; canWrite: boolean }) {
  const [open, setOpen] = useState(false);
  const [preset, setPreset] = useState('30');
  const [dialog, setDialog] = useState<'preset' | 'custom' | null>(null);
  const trigger = useRef<HTMLElement>(null);
  const [history, setHistory] = useState<SliBackfillPlan | null>(null);
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  const [retryExhausted, setRetryExhausted] = useState(false);
  const [jobs, setJobs] = useState<SliBackfillJob[]>([]);
  const [preview, setPreview] = useState<SliBackfillAdmission | null>(null);
  const [working, setWorking] = useState('');
  const [error, setError] = useState('');
  const [loadError, setLoadError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const requestKey = useRef('');
  const uncertainRequest = useRef<{ selection: string; key: string } | null>(null);
  const pendingRequest = useRef<AbortController | null>(null);
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
        const progress = JSON.stringify(jobs.map(job => [job.id, job.state, job.committed]));
        if (progress !== lastProgress || Date.now() - lastCoverage >= 30_000) {
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
  }, [open, preset, sli, context]);

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

  async function requestRange(confirmed: boolean) {
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
      const selected = JSON.stringify([selection.start, selection.end, retry]);
      if (!confirmed) {
        requestKey.current = uncertainRequest.current?.selection === selected ? uncertainRequest.current.key : crypto.randomUUID();
      } else {
        uncertainRequest.current = { selection: selected, key: requestKey.current };
      }
      const key = confirmed ? requestKey.current : crypto.randomUUID();
      const result = await client.requestBackfill(sli.id, { ...selection, preview: !confirmed, retry_exhausted: retry }, context, key, controller.signal);
      if (controller.signal.aborted) return;
      if (confirmed) {
        uncertainRequest.current = null;
        setPreview(null);
        setDialog(null);
      } else {
        setPreview(result);
      }
    } catch (failure) {
      if (controller.signal.aborted) return;
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
          <Button variant="outline" onClick={event => { trigger.current = event.currentTarget; edit(); setRetryExhausted(false); setDialog('custom'); }}>Custom range</Button>
          <Button disabled={history?.eligible_intervals === 0} onClick={event => {
            trigger.current = event.currentTarget;
            edit();
            setDialog('preset');
            void requestRange(false);
          }}>Review recoverable gaps</Button>
        </div>}
      </div>
      {dialog === null && error && <p role="alert" className="sli-error">{error}</p>}
      {loadError && <p className="sli-error" role="alert">{loadError}</p>}
      {history && <p className="sli-muted">{history.datasource_retention_days ? `Datasource retention is ${history.datasource_retention_days} days.` : 'Datasource retention is unknown. Recovery is best effort.'} Almanac requests at most 30 days. Earliest recoverable interval starts {utc(history.recovery_start)}.</p>}
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
      <form className="sli-metadata-form" onSubmit={event => { event.preventDefault(); void requestRange(false); }}>
        {dialog === 'custom' && <div className="ui-form-grid">
          <TextField id="sli-backfill-start" type="datetime-local" step={1} label="Start (UTC)" required value={start} disabled={!!working} onChange={event => { setStart(event.target.value); edit(); }} />
          <TextField id="sli-backfill-end" type="datetime-local" step={1} label="End (UTC)" required value={end} disabled={!!working} onChange={event => { setEnd(event.target.value); edit(); }} />
        </div>}
        {dialog === 'custom' && context.session?.role === 'administrator' &&
          <Switch label="Include previously failed intervals" checked={retryExhausted} disabled={!!working}
            onChange={event => { setRetryExhausted(event.target.checked); edit(); }} />}
        {preview && <div role="status">
          <p>{utc(preview.start)} to {utc(preview.end)} · {preview.chunk_count} {preview.chunk_count === 1 ? 'chunk' : 'chunks'}</p>
          {preview.plan && <dl className="sli-backfill-preview-facts">
            <div><dt>Intervals to recover</dt><dd>{preview.plan.eligible_intervals}</dd></div>
            <div><dt>Already covered</dt><dd>{preview.plan.covered_intervals}</dd></div>
            <div><dt>Scheduled by automatic repair</dt><dd>{preview.plan.automatic_intervals}</dd></div>
            <div><dt>Queued in manual jobs</dt><dd>{preview.plan.manual_intervals}</dd></div>
            <div><dt>Outside recovery window</dt><dd>{preview.plan.outside_retention_intervals}</dd></div>
            <div><dt>Previous attempt failed</dt><dd>{preview.plan.previous_failure_intervals}</dd></div>
          </dl>}
          {preview.plan?.gaps.some(gap => gap.state === 'eligible') && <ul>
            {preview.plan.gaps.filter(gap => gap.state === 'eligible').map(gap => <li key={gap.start}>{utc(gap.start)} – {utc(gap.end)} · {gap.intervals} {gap.intervals === 1 ? 'interval' : 'intervals'}</li>)}
          </ul>}
        </div>}
        {working === 'preview' && <p role="status">Checking which intervals can be recovered…</p>}
        {error && <p className="sli-error" role="alert">{error}</p>}
        <div className="sli-actions">
          <Button variant="outline" onClick={closeDialog}>Close</Button>
          {dialog === 'custom' && <Button type="submit" variant="outline" pending={working === 'preview'}>Preview range</Button>}
          {preview && <Button disabled={preview.chunk_count === 0 || !!working} pending={working === 'confirm'} onClick={() => void requestRange(true)}>Queue backfill</Button>}
        </div>
      </form>
    </Dialog>
  </details>;
}
