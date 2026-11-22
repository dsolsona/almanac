import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { DatePicker } from '../components/ui/DatePicker.tsx';
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

function isActive(job: SliBackfillJob) {
  return job.state === 'queued' || job.state === 'running';
}

function BackfillJobs({ jobs, label, canWrite, working, onCancel }: {
  jobs: SliBackfillJob[];
  label: string;
  canWrite: boolean;
  working: string;
  onCancel: (id: string) => void;
}) {
  return <ul className="sli-backfill-jobs" aria-label={label}>
    {jobs.map(job => <li key={job.id}>
      <div>
        <strong>{states[job.state]}</strong>{job.automatic && <span> · Automatic repair</span>}
        <p>{utc(job.start)} to {utc(job.end)}</p>
        <p>{job.recovered_intervals ?? 0} / {job.requested_intervals ?? 0} intervals recovered · {job.committed} / {job.chunks} chunks committed{job.gaps > 0 && ` · ${job.gaps} with gaps`}</p>
        {job.canonical_conflict && <p>Historical counts differed. Almanac kept the existing observations.</p>}
      </div>
      {canWrite && isActive(job) && <Button variant="outline" disabled={!!working} pending={working === job.id} onClick={() => onCancel(job.id)}>Cancel job</Button>}
    </li>)}
  </ul>;
}

export function SliBackfills({ sli, context, canWrite }: { sli: Sli; context: AuthContext; canWrite: boolean }) {
  const [open, setOpen] = useState(false);
  const [preset, setPreset] = useState('30');
  const [dialog, setDialog] = useState<'preset' | 'custom' | null>(null);
  const trigger = useRef<HTMLElement>(null);
  const [history, setHistory] = useState<SliBackfillPlan | null>(null);
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  const [startTime, setStartTime] = useState('');
  const [endTime, setEndTime] = useState('');
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
    if (working === 'confirm') setError('The request may have reached Almanac. Check Active backfills or Backfill job history for its status.');
  }

  async function requestRange(confirmed: boolean) {
    if (pendingRequest.current) return;
    const controller = new AbortController();
    pendingRequest.current = controller;
    setError('');
    setWorking(confirmed ? 'confirm' : 'preview');
    try {
      const selection = confirmed && preview ? { start: preview.start, end: preview.end }
        : dialog === 'custom' ? { start: new Date(`${start}T${startTime}Z`).toISOString(), end: new Date(`${end}T${endTime}Z`).toISOString() }
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
      setError(failure instanceof RangeError ? 'Choose a start and end date and enter both UTC times.' : failure instanceof Error ? failure.message : 'Could not request historical collection.');
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

  const today = new Date().toISOString().slice(0, 10);
  const activeJobs = jobs.filter(isActive);
  const finishedJobs = jobs.filter(job => !isActive(job));
  const historicalGaps = history?.gaps ?? [];
  const orderedGaps = [
    ...historicalGaps.filter(gap => gap.state === 'eligible'),
    ...historicalGaps.filter(gap => gap.state !== 'eligible'),
  ];
  const recoverableGaps = preview?.plan?.gaps.filter(gap => gap.state === 'eligible') ?? [];
  const listedRecoverableIntervals = recoverableGaps.reduce((sum, gap) => sum + gap.intervals, 0);
  const skippedCounts = preview?.plan ? [
    ['Already covered', preview.plan.covered_intervals],
    ['Scheduled by automatic repair', preview.plan.automatic_intervals],
    ['Queued in manual jobs', preview.plan.manual_intervals],
    ['Outside recovery window', preview.plan.outside_retention_intervals],
    ['Previous attempt failed', preview.plan.previous_failure_intervals],
  ] as const : [];

  return <details id="sli-backfills" className="sli-detail-disclosure" onToggle={event => { if (event.target === event.currentTarget) setOpen(event.currentTarget.open); }}>
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
      {history && <>
        {history.gaps.length ? <div className="sli-detail-table-wrap" role="region" aria-label="Historical gaps" tabIndex={0}>
          <table className="sli-intervals-table"><thead><tr><th scope="col">Gap (UTC)</th><th scope="col">Missing data</th><th scope="col">Recovery</th></tr></thead>
            <tbody>{orderedGaps.map(gap => <tr key={gap.start}><td>{utc(gap.start)} – {utc(gap.end)}</td><td>{elapsed(gap.intervals * history.cadence_seconds)} · {gap.intervals} intervals</td><td>{gapStates[gap.state]}</td></tr>)}</tbody>
          </table>
        </div> : <p>No settled intervals are missing in this period.</p>}
        {history.gap_count > history.gaps.length && <p className="sli-muted">Showing {history.gaps.length} of {history.gap_count} gaps from the most recent part of this period.</p>}
      </>}
      {activeJobs.length > 0 && <section className="sli-backfill-active">
        <h3>Active backfills</h3>
        <BackfillJobs jobs={activeJobs} label="Active backfills" canWrite={canWrite} working={working} onCancel={id => void cancel(id)} />
      </section>}
      {finishedJobs.length > 0 && <details className="sli-backfill-history">
        <summary>Backfill job history <span>({finishedJobs.length})</span></summary>
        <BackfillJobs jobs={finishedJobs} label="Backfill job history" canWrite={canWrite} working={working} onCancel={id => void cancel(id)} />
      </details>}
      {loaded && jobs.length === 0 && <p className="sli-muted">No backfill jobs yet.</p>}
    </div>
    <Dialog open={dialog !== null} onClose={closeDialog} title={dialog === 'custom' ? 'Custom backfill range' : 'Review backfill'} triggerRef={trigger}>
      <form className="sli-metadata-form sli-backfill-form" onSubmit={event => { event.preventDefault(); void requestRange(false); }}>
        {dialog === 'custom' && <div className="sli-backfill-range">
          <div className="ui-form-grid">
            <DatePicker label="Start date (UTC)" value={start} today={today} disabled={!!working} onValueChange={value => { setStart(value); edit(); }} />
            <TextField id="sli-backfill-start-time" type="time" step={1} label="Start time (UTC)" required value={startTime} disabled={!!working} onChange={event => { setStartTime(event.target.value); edit(); }} />
          </div>
          <div className="ui-form-grid">
            <DatePicker label="End date (UTC)" value={end} today={today} disabled={!!working} onValueChange={value => { setEnd(value); edit(); }} />
            <TextField id="sli-backfill-end-time" type="time" step={1} label="End time (UTC)" required value={endTime} disabled={!!working} onChange={event => { setEndTime(event.target.value); edit(); }} />
          </div>
        </div>}
        {dialog === 'custom' && context.session?.role === 'administrator' &&
          <Switch label="Include previously failed intervals" checked={retryExhausted} disabled={!!working}
            onChange={event => { setRetryExhausted(event.target.checked); edit(); }} />}
        {preview && <div className="sli-backfill-preview">
          <div className="sli-backfill-preview-summary" role="status">
            {preview.plan && <p>Intervals to recover <strong>{preview.plan.eligible_intervals}</strong></p>}
            <p>{preview.chunk_count} {preview.chunk_count === 1 ? 'chunk' : 'chunks'}</p>
          </div>
          <dl className="sli-backfill-preview-facts" aria-label="Selected range">
            <div><dt>Start</dt><dd>{utc(preview.start)}</dd></div>
            <div><dt>End</dt><dd>{utc(preview.end)}</dd></div>
          </dl>
          {recoverableGaps.length > 0 && <section className="sli-backfill-preview-gaps">
            <h3>Recoverable gaps</h3>
            <ul aria-label="Recoverable gap ranges" tabIndex={0}>
              {recoverableGaps.map(gap => <li key={gap.start}>
                <div>
                  <time dateTime={gap.start}>{utc(gap.start)}</time>
                  <time dateTime={gap.end}>to {utc(gap.end)}</time>
                </div>
                <span>{gap.intervals} {gap.intervals === 1 ? 'interval' : 'intervals'}</span>
              </li>)}
            </ul>
          </section>}
          {preview.plan && listedRecoverableIntervals < preview.plan.eligible_intervals &&
            <p className="sli-muted">The totals include additional recoverable gaps in the selected range.</p>}
          {skippedCounts.some(([, count]) => count > 0) && <details className="sli-backfill-history">
            <summary>Intervals skipped</summary>
            <dl className="sli-backfill-preview-facts">
              {skippedCounts.filter(([, count]) => count > 0).map(([label, count]) => <div key={label}><dt>{label}</dt><dd>{count}</dd></div>)}
            </dl>
          </details>}
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
