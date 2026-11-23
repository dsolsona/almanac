import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { SliMeasurementsChart } from '../components/charts/SliMeasurementsChart.tsx';
import { createSliClient, type SliMeasurementPeriod, type SliMeasurementSeries } from './client.ts';
import { summarizeMeasurementBuckets } from './measurements.ts';

const client = createSliClient();
const periodHours: Record<SliMeasurementPeriod, number> = { '3h': 3, '6h': 6, '24h': 24, '7d': 7 * 24, '30d': 30 * 24 };

export function SliMeasurementHistory({ id, period, cadence, refresh, latest }: { id: string; period: SliMeasurementPeriod; cadence: number; refresh: number; latest: ReactNode }) {
  const [series, setSeries] = useState<SliMeasurementSeries | null>(null);
  const [failed, setFailed] = useState(false);
  const [pending, setPending] = useState(true);
  useEffect(() => {
    const request = new AbortController();
    setFailed(false);
    setPending(true);
    void client.measurements(id, period, request.signal).then(value => {
      if (!request.signal.aborted) setSeries(value);
    }).catch(() => {
      if (!request.signal.aborted) setFailed(true);
    }).finally(() => {
      if (!request.signal.aborted) setPending(false);
    });
    return () => request.abort();
  }, [id, period, refresh]);
  const summary = useMemo(() => series && summarizeMeasurementBuckets(series.buckets), [series]);
  const end = summary?.intervals.at(-1)?.end;
  const spanSeconds = Math.floor(periodHours[period] * 3600 / cadence) * cadence;
  const requestedStart = end === undefined ? undefined : Date.parse(end) - spanSeconds * 1000;
  const seconds = series?.bucket_seconds ?? 0;
  const resolution = seconds % 3600 === 0 ? `${seconds / 3600} h` : seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`;
  return <div aria-busy={pending}>
    {pending && <p className="sr-only" role="status">Loading measurements…</p>}
    {failed && <p className="sli-error" role="alert">{summary ? 'Measurements could not be refreshed. Showing the last loaded values.' : 'Measurements could not be loaded. Refresh the SLI to try again.'}</p>}
    {summary && (summary.intervals.length ? <>
      <dl className="sli-measurement-summary">
        {latest}
        <div><dt>Observed requests</dt><dd>{summary.ratio === null ? '—' : `${summary.ratio}%`}</dd><p>Good events ÷ total observed events</p></div>
        <div><dt>Data coverage</dt><dd>{summary.covered} <small>/ {summary.expected} intervals</small></dd><p>{summary.failed} query errors · {summary.missing} missing · {summary.incomplete} partial</p></div>
      </dl>
      <SliMeasurementsChart intervals={summary.intervals} bucketed requestedStart={requestedStart}/>
      <p className="sli-chart-help">{resolution} per bucket · Ratios use observed requests only</p>
    </> : <p className="sli-muted">No intervals are due in this range.</p>)}
  </div>;
}
