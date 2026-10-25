import { validateTimeline } from '../../src/components/charts/timelineModel.ts';

const start = '9999-01-01T00:00:00Z';
const chart = {
  title: 'bounded regression', summary: 'test', start, end: '9999-01-01T00:00:00.002Z', stepMs: 0.01,
  lanes: [{ id: 'lane', label: 'Lane', unit: 'x', series: [{ id: 'series', label: 'Series', points: [
    { timestamp: start, value: '1', coverage: 'complete' },
    { timestamp: '9999-01-01T00:00:00.001Z', value: '2', coverage: 'complete' },
  ] }] }],
};

try {
  validateTimeline(chart);
  console.error('Expected an unrepresentable gap step to be rejected');
  process.exitCode = 1;
} catch (error) {
  console.log(error instanceof Error ? error.message : String(error));
}
