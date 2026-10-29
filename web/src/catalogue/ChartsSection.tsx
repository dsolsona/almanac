import { useEffect, useState, type CSSProperties } from 'react';
import { ExactValuesTable } from '../components/charts/ExactValuesTable.tsx';
import { TimelineChartView } from '../components/charts/TimelineChartView.tsx';
import { TimelineChartHeader } from '../components/charts/TimelineChartHeader.tsx';
import { SloBudgetSparkline } from '../components/charts/SloBudgetSparkline.tsx';
import { Panel } from '../components/ui/Panel.tsx';
import { StatusBadge } from '../components/ui/StatusBadge.tsx';
import { illustrativeChart, illustrativeRows } from './chartFixture.ts';

export function ChartsSection() {
  const [page, setPage] = useState(1);
  const [tableLane,setTableLane]=useState<string|null>(null);
  useEffect(() => { document.getElementById('charts')?.scrollIntoView({ block: 'start' }); }, []);
  const selectedLane=illustrativeChart.lanes.find(lane=>lane.id===tableLane);
  const selectedRows=illustrativeRows.filter(row=>row.lane===selectedLane?.label);
  return <section className="catalogue-section charts-section" id="charts" aria-labelledby="charts-heading">
    <div className="section-heading"><p className="eyebrow mono">08 / TIMELINES</p><h2 id="charts-heading">Aligned charts and exact values</h2><p>Illustrative synthetic fixtures only. No live data or SLO calculations are used. All four lanes share one UTC range; the table uses these same sample objects.</p></div>
    <div className="timeline-chart__header"><TimelineChartHeader chart={illustrativeChart}/></div>
    <div className="timeline-chart__frame" data-lanes={illustrativeChart.lanes.length} style={{'--timeline-lane-count':illustrativeChart.lanes.length} as CSSProperties}>
      <div className="timeline-chart__lane-controls" data-lanes={illustrativeChart.lanes.length} aria-label="Illustrative chart values">
        {illustrativeChart.lanes.map((lane,index)=><button key={lane.id} className="timeline-chart__lane-control" style={{gridRow:2+index*2}} type="button" aria-expanded={tableLane===lane.id} onClick={()=>setTableLane(current=>current===lane.id?null:lane.id)}><span>{lane.label}</span><strong>{['99.970000 %','87.120000 %','0.860000×','4 met · 1 violated'][index]}</strong><span>{tableLane===lane.id?'Hide table':'Table'}</span></button>)}
      </div>
      <TimelineChartView chart={illustrativeChart} showHeader={false} />
    </div>
    {selectedLane&&<ExactValuesTable rows={selectedRows.slice((page-1)*50,page*50)} page={page} totalRows={selectedRows.length} onPageChange={setPage}/>}
    <Panel heading="Rolling budget sparkline"><p className="mono">Illustrative values; missing and expired samples break the line.</p><SloBudgetSparkline label="Synthetic SLO budget remaining" samples={[{remaining_fraction:'0.8',state:'observed'},{remaining_fraction:'0.65',state:'observed'},{remaining_fraction:null,state:'missing'},{remaining_fraction:'0.42',state:'observed'}]}/></Panel>
    <div className="charts-section__states">
      <Panel heading="Empty state"><p>No samples are available in this illustrative range.</p></Panel>
      <Panel heading="Refresh error"><p className="ui-error" role="alert">The example refresh failed. Previously loaded values would be marked stale.</p><StatusBadge status="stale" /></Panel>
      <Panel heading="Stale data"><p>These examples are illustrative and are not being refreshed.</p><StatusBadge status="stale" /></Panel>
    </div>
  </section>;
}
