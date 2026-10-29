import type { TimelineChart } from './timelineModel.ts';

export function TimelineChartHeader({ chart, titleId }: { chart: TimelineChart; titleId?: string }) {
  return <>
    <h2 id={titleId}>{chart.title}</h2>
    <p className="timeline-chart__summary">{chart.summary}</p>
    <p className="timeline-chart__meta">Range: {chart.start} to {chart.end} ({chart.fillExpectedGaps===false?'source buckets are half-open; end-aligned evaluation points are shown':'end excluded'}) · {chart.timeZone==='local'?'local time':'UTC'} · step {Math.round(chart.stepMs / 1000)} seconds</p>
    <details className="timeline-chart__coverage"><summary>Coverage key</summary><p>Complete samples form continuous lines. Partial samples break those lines; numeric partial samples use isolated diamonds. Missing, known-empty, expired, not-applicable and zero-traffic samples also break lines.</p></details>
  </>;
}
