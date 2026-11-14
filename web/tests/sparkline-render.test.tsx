import { act, cleanup, render } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { BudgetSparklinePlot } from '../src/components/charts/BudgetSparklinePlot.tsx';

const chart=vi.hoisted(()=>({on:vi.fn(),setOption:vi.fn(),resize:vi.fn(),dispose:vi.fn(),getWidth:vi.fn(),getHeight:vi.fn()}));
vi.mock('echarts/core',async original=>({...await original<typeof import('echarts/core')>(),init:()=>chart}));
afterEach(()=>{cleanup();vi.unstubAllGlobals();vi.clearAllMocks();});

it('avoids unchanged observer redraws without skipping real resizes or initial readiness',()=>{
  let notify:ResizeObserverCallback;
  const disconnect=vi.fn();
  vi.stubGlobal('ResizeObserver',class {
    constructor(callback:ResizeObserverCallback){notify=callback;}
    observe(){}
    disconnect=disconnect;
  });
  chart.getWidth.mockReturnValue(140);chart.getHeight.mockReturnValue(24);
  const ready=vi.fn();
  const view=render(<BudgetSparklinePlot samples={[{remaining_fraction:'0.8',state:'observed'}]} label="Budget" onReady={ready}/>);
  const element=view.getByRole('img');
  const size=(width:number,height:number)=>act(()=>{
    const box=[{inlineSize:width,blockSize:height}];
    notify!([{target:element,contentRect:new DOMRectReadOnly(0,0,width,height),
      borderBoxSize:box,contentBoxSize:box,devicePixelContentBoxSize:box}],{observe(){},unobserve(){},disconnect});
  });
  size(140,24);
  expect(chart.resize).not.toHaveBeenCalled();
  expect(ready).not.toHaveBeenCalled();
  expect(element).toHaveAttribute('data-chart-ready','false');
  act(chart.on.mock.calls[0]![1] as ()=>void);
  expect(ready).toHaveBeenCalledOnce();
  size(140,24);
  expect(chart.resize).not.toHaveBeenCalled();
  size(180,24);
  expect(chart.resize).toHaveBeenCalledTimes(1);
  chart.getWidth.mockReturnValue(180);
  size(180,24);
  expect(chart.resize).toHaveBeenCalledTimes(1);
  size(180,36);
  expect(chart.resize).toHaveBeenCalledTimes(2);
  view.unmount();
  expect(disconnect).toHaveBeenCalledOnce();
  expect(chart.dispose).toHaveBeenCalledOnce();
});

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
