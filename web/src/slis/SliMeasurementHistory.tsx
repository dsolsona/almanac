import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { SliMeasurementsChart } from '../components/charts/SliMeasurementsChart.tsx';
import { createSliClient, type SliMeasurementPeriod, type SliMeasurementSeries } from './client.ts';
import { summarizeMeasurementBuckets } from './measurements.ts';

const client = createSliClient();

export function SliMeasurementHistory({ id, period, refresh, latest }: { id: string; period: SliMeasurementPeriod; refresh: number; latest: ReactNode }) {
  const [series, setSeries] = useState<SliMeasurementSeries | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const request = new AbortController();
    setFailed(false);
    setSeries(null);
    void client.measurements(id, period, request.signal).then(value => {
      if (!request.signal.aborted) setSeries(value);
    }).catch(() => {
      if (!request.signal.aborted) setFailed(true);
    });
    return () => request.abort();
  }, [id, period, refresh]);
  const summary = useMemo(() => series && summarizeMeasurementBuckets(series.buckets), [series]);
  if (failed) return <p className="sli-error" role="alert">Measurements could not be loaded. Refresh the SLI to try again.</p>;
  if (!summary) return <p className="sli-muted" role="status">Loading measurements…</p>;
  if (!summary.intervals.length) return <p className="sli-muted">No intervals are due in this range.</p>;

  const seconds = series!.bucket_seconds;
  const resolution = seconds % 3600 === 0 ? `${seconds / 3600} h` : seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`;
  return <>
    <dl className="sli-measurement-summary">
      {latest}
      <div><dt>Observed requests</dt><dd>{summary.ratio === null ? '—' : `${summary.ratio}%`}</dd><p>Good events ÷ total observed events</p></div>
      <div><dt>Data coverage</dt><dd>{summary.covered} <small>/ {summary.expected} intervals</small></dd><p>{summary.failed} query errors · {summary.missing} missing · {summary.incomplete} partial</p></div>
    </dl>
    <SliMeasurementsChart intervals={summary.intervals} bucketed/>
    <p className="sli-chart-help">{resolution} per bucket · Ratios use observed requests only</p>
  </>;
}
