import type { TimelineChart, TimelinePoint } from '../components/charts/timelineModel.ts';
import type { ExactValueRow } from '../components/charts/ExactValuesTable.tsx';

const start = Date.parse('2026-10-26T00:00:00Z');
export const intentionallyOmittedTimestamp = new Date(start + 8 * 86_400_000).toISOString();
const lanes = [
  { id: 'sli', label: 'SLI', unit: '%', base: 99.97 },
  { id: 'budget', label: 'Error budget', unit: '%', base: 87.12 },
  { id: 'burn', label: 'Burn rate', unit: 'x', base: 0.86 },
  { id: 'compliance', label: 'Daily compliance', unit: '%', base: 99 },
];

const dailyStatuses = ['met','met','violated','unknown','met','not_applicable','met'] as const;
const dailyAttainment = [99.8,99.4,98.7,99.6,99.9,null,99.3] as const;
const pointsFor = (lane: typeof lanes[number]): TimelinePoint[] => Array.from({length:60},(_,index)=>index)
  .filter(index=>index!==8)
  .map(index=>{
    if(lane.id==='compliance'){
      const status=dailyStatuses[index%dailyStatuses.length]!,attainment=dailyAttainment[index%dailyAttainment.length]!;
      return {timestamp:new Date(start+index*86_400_000).toISOString(),value:attainment===null?null:attainment.toFixed(6),coverage:status==='unknown'?'partial':status==='not_applicable'?'not-applicable':'complete'};
    }
    const coverage = index === 4 ? 'partial' : index === 5 ? 'missing' : index === 6 ? 'complete' : index === 10 ? 'zero-traffic'
      : index === 11 ? 'known-empty' : index === 12 ? 'expired' : index === 13 ? 'not-applicable' : 'complete';
    const numeric = index === 6 ? 0 : lane.base - (index % 3) * 0.01;
    return {
      timestamp: new Date(start + index * 86_400_000).toISOString(),
      value: coverage === 'complete' || coverage === 'partial' ? numeric.toFixed(6) : null,
      coverage,
    };
  });

export const illustrativeChart: TimelineChart = {
  title: 'Illustrative service signals',
  summary: 'Synthetic catalogue samples for chart and exact-value table demonstrations. No live data or SLO calculations are used. Daily compliance shows numerical rolling attainment at 59 recorded UTC day ends, with met, violated, unknown and not-applicable outcomes kept distinct. One timestamp is deliberately absent from every lane to demonstrate a shared gap. All lanes share one 60-day UTC range; exact tables use these same samples.',
  start: new Date(start).toISOString(), end: new Date(start + 60 * 86_400_000).toISOString(), stepMs: 86_400_000,
  referenceLines: [{lane:'sli',value:'99',label:'Target 99%'},{lane:'budget',value:'0',label:'0% remaining'},{lane:'burn',value:'1',label:'1×'},{lane:'compliance',value:'99',label:'Target 99%'}],
  lanes: lanes.map((lane) => ({ id: lane.id, label: lane.label, unit: lane.unit, series: [{ id: `${lane.id}-example`, label: 'Example service', points: pointsFor(lane) }] })),
};

export const illustrativeRows: ExactValueRow[] = illustrativeChart.lanes.flatMap((lane) => lane.series.flatMap((series) => series.points.map((point) => ({
  timestamp: point.timestamp, lane: lane.label, series: series.label, unit: lane.unit, value: point.value, coverage: point.coverage,
  ...(lane.id === 'compliance' ? {details:`${dailyStatuses[Math.round((Date.parse(point.timestamp)-start)/86_400_000)%dailyStatuses.length]??'unknown'} · requested ${point.timestamp} · effective ${point.timestamp} · target 99% · rolling attainment ${point.value??'unavailable'}`} : {}),
})))).sort((a, b) => a.timestamp.localeCompare(b.timestamp) || a.lane.localeCompare(b.lane));
