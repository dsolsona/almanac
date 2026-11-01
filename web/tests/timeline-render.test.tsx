import { act, cleanup, render } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { illustrativeChart } from '../src/catalogue/chartFixture.ts';
import { TimelineChartView } from '../src/components/charts/TimelineChartView.tsx';

const chart = vi.hoisted(() => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn() }));
vi.mock('echarts/core', async importOriginal => ({ ...await importOriginal<typeof import('echarts/core')>(), init: () => chart }));

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.clearAllMocks(); });

it('draws once on mount and initial size notification, then redraws changed data and dimensions', () => {
  let notify = () => {};
  const disconnect = vi.fn();
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: () => void) { notify = callback; }
    observe() {}
    disconnect = disconnect;
  });
  const view = render(<TimelineChartView chart={illustrativeChart} />);
  act(notify);
  expect(chart.setOption).toHaveBeenCalledTimes(1);
  expect(chart.resize).not.toHaveBeenCalled();
  view.rerender(<TimelineChartView chart={{ ...illustrativeChart, summary: 'Updated measurements' }} />);
  expect(chart.setOption).toHaveBeenCalledTimes(2);
  const host = view.container.querySelector('.timeline-chart__plot')!;
  Object.defineProperty(host, 'clientWidth', { value: 800 });
  act(notify);
  expect(chart.resize).toHaveBeenCalledTimes(1);
  expect(chart.setOption).toHaveBeenCalledTimes(3);
  act(notify);
  expect(chart.setOption).toHaveBeenCalledTimes(3);
  view.unmount();
  expect(disconnect).toHaveBeenCalledOnce();
  expect(chart.dispose).toHaveBeenCalledOnce();
});
