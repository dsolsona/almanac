import { act, cleanup, render } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { BudgetSparklinePlot } from '../src/components/charts/BudgetSparklinePlot.tsx';

const chart=vi.hoisted(()=>({on:vi.fn(),setOption:vi.fn(),resize:vi.fn(),dispose:vi.fn()}));
vi.mock('echarts/core',async original=>({...await original<typeof import('echarts/core')>(),init:()=>chart}));
afterEach(()=>{cleanup();vi.unstubAllGlobals();vi.clearAllMocks();});

it('signals completion only when the renderer finishes and uses the latest callback for updates',()=>{
  vi.stubGlobal('ResizeObserver',class {observe(){}disconnect(){}});
  const first=vi.fn(),second=vi.fn();
  const samples=[{remaining_fraction:'0.8',state:'observed'}];
  const view=render(<BudgetSparklinePlot samples={samples} label="Budget" onReady={first}/>);
  expect(chart.on).toHaveBeenCalledWith('finished',expect.any(Function));
  expect(chart.setOption).toHaveBeenCalledTimes(1);
  expect(first).not.toHaveBeenCalled();
  expect(view.getByRole('img')).toHaveAttribute('data-chart-ready','false');
  const finished=chart.on.mock.calls[0]![1] as ()=>void;
  act(finished);
  expect(first).toHaveBeenCalledOnce();
  expect(view.getByRole('img')).toHaveAttribute('data-chart-ready','true');
  view.rerender(<BudgetSparklinePlot samples={[{remaining_fraction:'0.4',state:'partial'}]} label="Budget" onReady={second}/>);
  expect(second).not.toHaveBeenCalled();
  expect(view.getByRole('img')).toHaveAttribute('data-chart-ready','false');
  act(finished);
  expect(second).toHaveBeenCalledOnce();
  expect(chart.setOption).toHaveBeenCalledTimes(2);
  view.unmount();
  expect(chart.dispose).toHaveBeenCalledOnce();
});
