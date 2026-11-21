import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button } from '../components/ui/Button.tsx';
import { SelectField } from '../components/ui/SelectField.tsx';
import { TextField } from '../components/ui/TextField.tsx';
import { createSliClient, type Sli, type SliBackfillAdmission, type SliBackfillJob } from './client.ts';

const client = createSliClient();
const presets = [
  { value: '1', label: 'Last 1 day' },
  { value: '7', label: 'Last 7 days' },
  { value: '14', label: 'Last 14 days' },
  { value: '30', label: 'Last 30 days' },
  { value: 'custom', label: 'Custom range' },
];

function rangeFor(sli: Sli, preset: string) {
  const cadence = sli.polling_interval_seconds * 1_000;
  const end = Math.floor((Date.now() - (sli.settling_delay_seconds + 120) * 1_000) / cadence) * cadence;
  const requested = end - Number(preset) * 86_400_000;
  const start = Math.ceil(Math.max(requested, Date.parse(sli.effective_from)) / cadence) * cadence;
  return { start: new Date(start).toISOString(), end: new Date(end).toISOString(), shortened: start > requested };
}

function utc(value: string) {
  return new Date(value).toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC');
}

const states: Record<SliBackfillJob['state'], string> = {
  queued: 'Queued', running: 'Running', completed: 'Completed', 'completed-with-gaps': 'Completed with gaps', cancelled: 'Cancelled',
};

export function SliBackfills({ sli, context, canWrite }: { sli: Sli; context: AuthContext; canWrite: boolean }) {
  const [open, setOpen] = useState(false);
  const [preset, setPreset] = useState('1');
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  const [jobs, setJobs] = useState<SliBackfillJob[]>([]);
  const [preview, setPreview] = useState<SliBackfillAdmission | null>(null);
  const [shortened, setShortened] = useState(false);
  const [working, setWorking] = useState('');
  const [error, setError] = useState('');
  const [loadError, setLoadError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const requestKey = useRef('');

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const result = await client.backfills(sli.id, controller.signal);
        if (controller.signal.aborted) return;
        setJobs(result);
        setLoaded(true);
        setLoadError('');
      } catch {
        if (!controller.signal.aborted) setLoadError('Could not refresh backfill jobs.');
      }
      if (!controller.signal.aborted) timer = setTimeout(refresh, 3_000);
    }
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [open, sli.id]);

  function edit() {
    setPreview(null);
    setError('');
  }

  async function requestRange(confirmed: boolean) {
    setError('');
    setWorking(confirmed ? 'confirm' : 'preview');
    try {
      const selection = confirmed && preview ? { start: preview.start, end: preview.end, shortened }
        : preset === 'custom' ? { start: new Date(`${start}Z`).toISOString(), end: new Date(`${end}Z`).toISOString(), shortened: false }
          : rangeFor(sli, preset);
      if (!confirmed) requestKey.current = crypto.randomUUID();
      const result = await client.requestBackfill(sli.id, { start: selection.start, end: selection.end, preview: !confirmed }, context, requestKey.current);
      if (confirmed) {
        setPreview(null);
        setJobs(await client.backfills(sli.id));
      } else {
        setShortened(selection.shortened);
        setPreview(result);
      }
    } catch (failure) {
      setError(failure instanceof RangeError ? 'Enter both UTC dates.' : failure instanceof Error ? failure.message : 'Could not request historical collection.');
    } finally {
      setWorking('');
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

  return <details className="sli-detail-disclosure" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Historical backfill<span>Request missing data and inspect job progress</span></summary>
    <div className="sli-backfill-body">
      {canWrite && <form className="sli-metadata-form" onSubmit={event => { event.preventDefault(); void requestRange(false); }}>
        <SelectField id="sli-backfill-range" label="Range" value={preset} options={presets} disabled={!!working} onChange={event => { setPreset(event.target.value); edit(); }} />
        {preset === 'custom' && <div className="ui-form-grid">
          <TextField id="sli-backfill-start" type="datetime-local" step={1} label="Start (UTC)" required value={start} disabled={!!working} onChange={event => { setStart(event.target.value); edit(); }} />
          <TextField id="sli-backfill-end" type="datetime-local" step={1} label="End (UTC)" required value={end} disabled={!!working} onChange={event => { setEnd(event.target.value); edit(); }} />
        </div>}
        {preview && <div role="status">
          <p>{utc(preview.start)} to {utc(preview.end)} · {preview.chunk_count} {preview.chunk_count === 1 ? 'chunk' : 'chunks'}</p>
          {shortened && <p className="sli-muted">The range starts at the first valid measurement interval.</p>}
        </div>}
        <div className="sli-actions">
          <Button type="submit" variant="outline" disabled={!!working} pending={working === 'preview'}>Preview range</Button>
          {preview && <Button type="button" disabled={!!working} pending={working === 'confirm'} onClick={() => void requestRange(true)}>Request backfill</Button>}
        </div>
      </form>}
      {error && <p className="sli-error" role="alert">{error}</p>}
      {loadError && <p className="sli-error" role="alert">{loadError}</p>}
      {loaded && jobs.length === 0 && <p className="sli-muted">No backfill jobs yet.</p>}
      {jobs.length > 0 && <ul className="sli-backfill-jobs" aria-label="Backfill jobs">
        {jobs.map(job => <li key={job.id}>
          <div><strong>{states[job.state]}</strong>{job.automatic && <span> · Automatic repair</span>}<p>{utc(job.start)} to {utc(job.end)}</p>
            <p>{job.committed} / {job.chunks} chunks committed{job.gaps > 0 && ` · ${job.gaps} with gaps`}</p></div>
          {canWrite && ['queued', 'running'].includes(job.state) && <Button variant="outline" disabled={!!working} pending={working === job.id} onClick={() => void cancel(job.id)}>Cancel job</Button>}
        </li>)}
      </ul>}
    </div>
  </details>;
}
