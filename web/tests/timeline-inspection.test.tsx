import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { TimelineChartView } from '../src/components/charts/TimelineChartView.tsx';
import type { TimelineChart } from '../src/components/charts/timelineModel.ts';

vi.mock('echarts/core', async original => ({ ...await original<typeof import('echarts/core')>(), init: () => ({ on() {}, setOption() {}, resize() {}, dispose() {}, dispatchAction() {}, convertFromPixel: () => Date.parse('2026-11-12T10:30:00Z') }) }));
beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const chart: TimelineChart = { title: 'Burn evidence', summary: 'Supplied exact values.', start: '2026-11-12T10:00:00Z', end: '2026-11-12T11:00:00Z', stepMs: 1800000, fillExpectedGaps: false, lanes: [{ id: 'burn', label: 'Burn', unit: '×', series: [{ id: 'short', label: '30 min burn', points: [{ timestamp: '2026-11-12T10:00:00Z', value: '6.0000000000001', coverage: 'complete' }, { timestamp: '2026-11-12T10:30:00Z', value: null, coverage: 'missing', reason: 'No observations received' }] }, { id: 'long', label: '6 h burn', points: [{ timestamp: '2026-11-12T10:00:00Z', value: '5.9999999999999', coverage: 'complete' }, { timestamp: '2026-11-12T10:30:00Z', value: '7.4', coverage: 'complete' }] }] }] };

it('inspects exact multi-series values and gaps by keyboard, without rounded comparisons', async () => {
  const user = userEvent.setup(); render(<TimelineChartView chart={chart} inspection />);
  const plot = screen.getByRole('group', { name: /Inspect Burn evidence/ }); plot.focus();
  await user.keyboard('{Home}');
  expect(within(screen.getByRole('tooltip')).getByText('6.0000000000001 ×')).toBeVisible();
  expect(within(screen.getByRole('tooltip')).getByText('5.9999999999999 ×')).toBeVisible();
  await user.keyboard('{ArrowRight}');
  expect(screen.getByRole('tooltip')).toHaveTextContent('No observations received');
  expect(screen.getByRole('tooltip')).toHaveTextContent('7.4 ×');
  await user.keyboard('{Escape}'); expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
});

it('touch keeps selected evidence until outside dismissal; new evidence clears old inspection', () => {
  const view = render(<TimelineChartView chart={chart} inspection />);
  const plot = screen.getByRole('group', { name: /Inspect Burn evidence/ });
  fireEvent.pointerDown(plot, { pointerType: 'touch', clientX: 200, clientY: 100 });
  expect(screen.getByRole('tooltip')).toHaveTextContent('No observations received');
  fireEvent.pointerLeave(plot, { pointerType: 'touch' }); expect(screen.getByRole('tooltip')).toBeVisible();
  fireEvent.pointerDown(document.body); expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  fireEvent.keyDown(plot, { key: 'Home' }); expect(screen.getByRole('tooltip')).toBeVisible();
  view.rerender(<TimelineChartView chart={{ ...chart, summary: 'Updated evidence' }} inspection />);
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
});

it('leaves default chart consumers unfocusable and keeps invalid evidence actionable', () => {
  const view = render(<TimelineChartView chart={chart} />);
  expect(screen.queryByRole('group', { name: /Inspect/ })).not.toBeInTheDocument();
  view.rerender(<TimelineChartView chart={{ ...chart, end: chart.start }} inspection />);
  expect(screen.getByRole('alert')).toHaveTextContent('Chart unavailable');
});
