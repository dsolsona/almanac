import { useEffect, useState, type CSSProperties } from 'react';
import { ExactValuesTable } from '../components/charts/ExactValuesTable.tsx';
import { TimelineChartView } from '../components/charts/TimelineChartView.tsx';
import { TimelineChartHeader } from '../components/charts/TimelineChartHeader.tsx';
import { SloBudgetSparkline } from '../components/charts/SloBudgetSparkline.tsx';
import { Panel } from '../components/ui/Panel.tsx';
import { StatusBadge } from '../components/ui/StatusBadge.tsx';
import { illustrativeChart, illustrativeRows } from './chartFixture.ts';
import type { TimelineChart } from '../components/charts/timelineModel.ts';
import { SliMeasurementsChart } from '../components/charts/SliMeasurementsChart.tsx';
import { measurementRatio, type MeasurementInterval } from '../slis/measurements.ts';

const sliSamples:MeasurementInterval[]=Array.from({length:60},(_,i)=>{
  const state=i===52?'missing':i===43||i===44?'failed':'observed';
  const good=state==='observed'?String(i===29?1850:1998):null,total=state==='observed'?'2000':null;
  return {start:new Date(Date.parse('2026-11-11T09:00:00Z')+i*60_000).toISOString(),end:new Date(Date.parse('2026-11-11T09:00:00Z')+(i+1)*60_000).toISOString(),state,good,total,ratio:measurementRatio(good,total),detail:state==='failed'?'Query timed out':state==='missing'?'No observation received':''};
});
const sliOtherStates:MeasurementInterval[]=sliSamples.slice(0,4).map((row,i)=>({...row,state:(['zero','empty','partial','missing'] as const)[i]!,good:i===0?'0':null,total:i===0?'0':null,ratio:null,detail:['No events were recorded','Queries returned no count values','Missing total count','No observation received'][i]!}));

const coarseChart:TimelineChart={title:'Illustrative coarse coverage',summary:'Synthetic exact values in a period containing small gaps. The line breaks; qualified markers retain the values. Choose a shorter range for finer gap detail.',start:'2026-10-26T00:00:00Z',end:'2026-10-27T00:00:00Z',stepMs:86400000,fillExpectedGaps:false,lanes:[{id:'attainment',label:'SLI',unit:'%',height:162,series:[{id:'coarse',label:'Observed values',points:[1,12,24].map((hour,index)=>({timestamp:new Date(Date.parse('2026-10-26T00:00:00Z')+hour*3600000).toISOString(),value:['99.99','99.50','99.98'][index]!,coverage:'complete',displayCoverage:{start:'2026-10-26T00:00:00Z',end:'2026-10-27T00:00:00Z',states:['complete','missing']}}))}]}]};

const dailyCompliance = illustrativeChart.lanes.find(lane => lane.id === 'compliance');
const dailyCounts = dailyCompliance?.series.flatMap(series => series.points).reduce((counts, point) => {
  if (point.coverage === 'not-applicable') counts.notApplicable++;
  else if (point.coverage !== 'complete' || point.value === null) counts.unknown++;
  else if (Number(point.value) >= 99) counts.met++;
  else counts.violated++;
  return counts;
}, { met: 0, violated: 0, unknown: 0, notApplicable: 0 });

export function ChartsSection() {
  const [page, setPage] = useState(1);
  const [tableLane,setTableLane]=useState<string|null>(null);
  useEffect(() => { document.getElementById('charts')?.scrollIntoView({ block: 'start' }); }, []);
  const selectedLane=illustrativeChart.lanes.find(lane=>lane.id===tableLane);
  const selectedRows=illustrativeRows.filter(row=>row.lane===selectedLane?.label);
  return <section className="catalogue-section charts-section" id="charts" aria-labelledby="charts-heading">
    <div className="section-heading"><p className="eyebrow mono">08 / TIMELINES</p><h2 id="charts-heading">Aligned charts and exact values</h2><p>Illustrative synthetic fixtures only. No live data or SLO calculations are used. All four lanes share one UTC range and one deliberately omitted timestamp; the table uses these same sample objects.</p></div>
    <section className="timeline-chart" aria-label="Illustrative timeline chart">
      <div className="timeline-chart__header"><TimelineChartHeader chart={illustrativeChart}/></div>
      <div className="timeline-chart__frame" data-lanes={illustrativeChart.lanes.length} style={{'--timeline-lane-count':illustrativeChart.lanes.length} as CSSProperties}>
        <div className="timeline-chart__lane-controls" data-lanes={illustrativeChart.lanes.length} aria-label="Illustrative chart values">
          {illustrativeChart.lanes.map((lane,index)=><button key={lane.id} className="timeline-chart__lane-control" style={{gridRow:2+index*2}} type="button" aria-expanded={tableLane===lane.id} onClick={()=>setTableLane(current=>current===lane.id?null:lane.id)}><span>{lane.label}</span><strong>{lane.id === 'compliance' ? `${dailyCounts?.met ?? 0} met · ${dailyCounts?.violated ?? 0} violated · ${dailyCounts?.unknown ?? 0} unknown · ${dailyCounts?.notApplicable ?? 0} N/A` : ['99.970000 %','87.120000 %','0.860000×'][index]}</strong><span>{tableLane===lane.id?'Hide table':'Table'}</span></button>)}
        </div>
        <TimelineChartView chart={illustrativeChart} showHeader={false} />
      </div>
    </section>
    {selectedLane&&<ExactValuesTable rows={selectedRows.slice((page-1)*50,page*50)} page={page} totalRows={selectedRows.length} onPageChange={setPage}/>}
    <Panel heading="Rolling budget sparkline"><p className="mono">Illustrative values; missing and expired samples break the line.</p><SloBudgetSparkline label="Synthetic SLO budget remaining" samples={[{remaining_fraction:'0.8',state:'observed'},{remaining_fraction:'0.65',state:'observed'},{remaining_fraction:null,state:'missing'},{remaining_fraction:'0.42',state:'observed'}]}/></Panel>
    <Panel heading="Coarse coverage"><p>Illustrative values only. Shaded periods contain missing measurements. Markers retain exact selected values; choose a shorter range for finer gaps.</p><TimelineChartView chart={coarseChart}/></Panel>
    <Panel heading="SLI measurements and collection"><p>Illustrative received counts, query errors and a missing interval. Green means counts received. Hover, tap or focus the chart and use arrow keys for exact values.</p><SliMeasurementsChart intervals={sliSamples}/></Panel>
    <Panel heading="SLI intervals without a ratio"><p>Zero traffic, known empty, partial and missing remain distinct. None has an invented ratio.</p><SliMeasurementsChart intervals={sliOtherStates}/></Panel>
    <div className="charts-section__states">
      <Panel heading="Empty state"><p>No samples are available in this illustrative range.</p></Panel>
      <Panel heading="Refresh error"><p className="ui-error" role="alert">The example refresh failed. Previously loaded values would be marked stale.</p><StatusBadge status="stale" /></Panel>
      <Panel heading="Stale data"><p>These examples are illustrative and are not being refreshed.</p><StatusBadge status="stale" /></Panel>
    </div>
  </section>;
}
