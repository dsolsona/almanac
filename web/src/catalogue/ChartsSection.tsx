import { useEffect, useState } from 'react';
import { ExactValuesTable } from '../components/charts/ExactValuesTable.tsx';
import { TimelineChartView } from '../components/charts/TimelineChartView.tsx';
import { Panel } from '../components/ui/Panel.tsx';
import { StatusBadge } from '../components/ui/StatusBadge.tsx';
import { illustrativeChart, illustrativeRows } from './chartFixture.ts';

export function ChartsSection() {
  const [page, setPage] = useState(1);
  useEffect(() => { document.getElementById('charts')?.scrollIntoView({ block: 'start' }); }, []);
  return <section className="catalogue-section charts-section" id="charts" aria-labelledby="charts-heading">
    <div className="section-heading"><p className="eyebrow mono">08 / TIMELINES</p><h2 id="charts-heading">Aligned charts and exact values</h2><p>Illustrative synthetic fixtures only. No live data or SLO calculations are used. All four lanes share one UTC range; the table uses these same sample objects.</p></div>
    <TimelineChartView chart={illustrativeChart} />
    <ExactValuesTable rows={illustrativeRows.slice((page - 1) * 50, page * 50)} page={page} totalRows={illustrativeRows.length} onPageChange={setPage} />
    <div className="charts-section__states">
      <Panel heading="Empty state"><p>No samples are available in this illustrative range.</p></Panel>
      <Panel heading="Refresh error"><p className="ui-error" role="alert">The example refresh failed. Previously loaded values would be marked stale.</p><StatusBadge status="stale" /></Panel>
      <Panel heading="Stale data"><p>These examples are illustrative and are not being refreshed.</p><StatusBadge status="stale" /></Panel>
    </div>
  </section>;
}
