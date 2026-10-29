import type { TimelineChart, TimelinePoint } from '../components/charts/timelineModel.ts';
import type { ExactValueRow } from '../components/charts/ExactValuesTable.tsx';

const start = Date.parse('2026-10-25T00:00:00Z');
const lanes = [
  { id: 'sli', label: 'SLI', unit: '%', base: 99.97 },
  { id: 'budget', label: 'Error budget', unit: '%', base: 87.12 },
  { id: 'burn', label: 'Burn rate', unit: 'x', base: 0.86 },
  { id: 'compliance', label: 'Daily compliance', unit: '%', base: 99 },
];

const dailyStatuses = ['met','met','violated','unknown','met','not_applicable','met'] as const;
const dailyAttainment = [99.8,99.4,98.7,99.6,99.9,null,99.3] as const;
const pointsFor = (lane: typeof lanes[number]): TimelinePoint[] => Array.from({ length: 7 }, (_, index) => index)
  .map((index) => {
    const dailyCoverage = dailyStatuses[index] === 'unknown' ? 'partial' : dailyStatuses[index] === 'not_applicable' ? 'not-applicable' : 'complete';
    const coverage = lane.id === 'compliance' ? dailyCoverage : index === 3 ? 'partial' : index === 4 ? 'missing' : index === 5 ? 'expired' : 'complete';
    const numeric = lane.id === 'compliance' ? dailyAttainment[index]??null
      : lane.base - (index % 3) * 0.01;
    return {
      timestamp: new Date(start + (index + 1) * 86_400_000).toISOString(),
      value: numeric === null || coverage !== 'complete' && coverage !== 'partial' ? null : numeric.toFixed(6),
      coverage,
    };
  });

export const illustrativeChart: TimelineChart = {
  title: 'Illustrative service signals',
  summary: 'Synthetic catalogue samples for chart and exact-value table demonstrations. Daily compliance points show rolling attainment at completed UTC day ends; outcome and qualification remain separate. These are not live observations or SLO calculations.',
  start: new Date(start).toISOString(), end: new Date(start + 7 * 86_400_000).toISOString(), stepMs: 86_400_000,
  referenceLines: [{lane:'sli',value:'99',label:'Target 99%'},{lane:'budget',value:'0',label:'0% remaining'},{lane:'burn',value:'1',label:'1×'},{lane:'compliance',value:'99',label:'Target 99%'}],
  lanes: lanes.map((lane) => ({ id: lane.id, label: lane.label, unit: lane.unit, series: [{ id: `${lane.id}-example`, label: 'Example service', points: pointsFor(lane) }] })),
};

export const illustrativeRows: ExactValueRow[] = illustrativeChart.lanes.flatMap((lane) => lane.series.flatMap((series) => series.points.map((point) => ({
  timestamp: point.timestamp, lane: lane.label, series: series.label, unit: lane.unit, value: point.value, coverage: point.coverage,
  ...(lane.id === 'compliance' ? {details:`${dailyStatuses[Math.round((Date.parse(point.timestamp)-start)/86_400_000)-1]??'unknown'} · requested ${point.timestamp} · effective ${point.timestamp} · target 99% · rolling attainment ${point.value??'unavailable'}`} : {}),
})))).sort((a, b) => a.timestamp.localeCompare(b.timestamp) || a.lane.localeCompare(b.lane));
