import type { TimelineChart, TimelinePoint } from '../components/charts/timelineModel.ts';
import type { ExactValueRow } from '../components/charts/ExactValuesTable.tsx';

const start = Date.parse('2026-10-25T00:00:00Z');
const lanes = [
  { id: 'sli', label: 'SLI', unit: '%', base: 99.97 },
  { id: 'budget', label: 'Error budget', unit: '%', base: 87.12 },
  { id: 'burn', label: 'Burn rate', unit: 'x', base: 0.86 },
  { id: 'coverage', label: 'Coverage', unit: '%', base: 92.5 },
];

const pointsFor = (lane: typeof lanes[number]): TimelinePoint[] => Array.from({ length: 16 }, (_, index) => index)
  .filter((index) => index !== 8)
  .map((index) => {
    const coverage = index === 4 ? 'partial' : index === 5 ? 'missing' : index === 10 ? 'zero-traffic'
      : index === 11 ? 'known-empty' : index === 12 ? 'expired' : index === 13 ? 'not-applicable' : 'complete';
    const numeric = index === 6 ? 0 : lane.base - (index % 3) * 0.01;
    return {
      timestamp: new Date(start + index * 60_000).toISOString(),
      value: coverage === 'complete' || coverage === 'partial' ? numeric.toFixed(6) : null,
      coverage,
    };
  });

export const illustrativeChart: TimelineChart = {
  title: 'Illustrative service signals',
  summary: 'Synthetic catalogue samples for chart and exact-value table demonstrations. These are not live observations or SLO calculations.',
  start: new Date(start).toISOString(), end: new Date(start + 16 * 60_000).toISOString(), stepMs: 60_000,
  lanes: lanes.map((lane) => ({ id: lane.id, label: lane.label, unit: lane.unit, series: [{ id: `${lane.id}-example`, label: 'Example service', points: pointsFor(lane) }] })),
};

export const illustrativeRows: ExactValueRow[] = illustrativeChart.lanes.flatMap((lane) => lane.series.flatMap((series) => series.points.map((point) => ({
  timestamp: point.timestamp, lane: lane.label, series: series.label, unit: lane.unit, value: point.value, coverage: point.coverage,
})))).sort((a, b) => a.timestamp.localeCompare(b.timestamp) || a.lane.localeCompare(b.lane));
