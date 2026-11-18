import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { SloHistoryChart, pointTime } from '../src/components/charts/SloHistoryChart.tsx';
import { SloMaintenanceList } from '../src/slos/SloMaintenanceTimeline.tsx';
import type { components } from '../src/api/generated.ts';
beforeEach(()=>vi.stubGlobal('ResizeObserver',class{observe(){}disconnect(){}}));
afterEach(()=>{cleanup();vi.unstubAllGlobals();vi.unstubAllEnvs();});
const sample=(time:string,value:string|null,state:components['schemas']['SloOperationalSample']['state']='observed')=>({evaluation_at:time,bucket_start:time,bucket_end:time,state,allowed_budget:null,remaining_budget:null,remaining_fraction:value,attainment:value,burn:value,good:'9007199254740993.000000000001',total:'9007199254740994.000000000002',quality:{state:state==='observed'?'complete':'partial',freshness:'fresh',expected_intervals:1,observed_intervals:1,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:'1',first_observation_at:time,last_observation_at:time,latest_receipt_at:time},retained_extrema:[],revision:'1',received_at:time}) as components['schemas']['SloOperationalSample'];
it('inspects exact values and timestamps without inventing numeric points across gaps',()=>{
 const samples=[sample('2026-11-06T10:00:00Z','0.85'),sample('2026-11-09T10:00:00Z',null,'missing'),sample('2026-11-12T10:00:00Z','-0.2')];
 render(<SloHistoryChart kind="budget" samples={samples} start="2026-11-06T10:00:00Z" end="2026-11-12T10:00:00Z" target="0.999"/>);
 const chart=screen.getByRole('group',{name:/Inspect/});chart.focus();fireEvent.keyDown(chart,{key:'End'});
 expect(screen.getByRole('tooltip')).toHaveTextContent('-20%');expect(screen.getByRole('tooltip')).toHaveTextContent('12 Nov 2026');
 fireEvent.keyDown(chart,{key:'ArrowLeft'});expect(screen.getByRole('tooltip')).toHaveTextContent('Missing');expect(document.querySelector('.inspection-dot')).not.toBeInTheDocument();
 expect(document.querySelectorAll('.curve')).toHaveLength(2);
 fireEvent.keyDown(chart,{key:'Home'});expect(screen.getByRole('tooltip')).toHaveTextContent('85%');
 fireEvent.keyDown(chart,{key:'Escape'});expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
});
it('does not fabricate zero burn or a successful empty chart',()=>{
 render(<SloHistoryChart kind="burn" samples={[]} start="2026-11-06T10:00:00Z" end="2026-11-12T10:00:00Z" target="0.999"/>);
 expect(screen.getByText('No history available for this period.')).toBeVisible();expect(document.querySelector('.curve')).not.toBeInTheDocument();
});
it('labels excluded evidence without drawing a successful point',()=>{
 const point=sample('2026-11-09T10:00:00Z',null,'excluded');point.quality.state='excluded';
 render(<SloHistoryChart kind="burn" samples={[point]} start="2026-11-06T10:00:00Z" end="2026-11-12T10:00:00Z" target="0.999"/>);
 fireEvent.keyDown(screen.getByRole('group',{name:/Inspect/}),{key:'Home'});
 expect(screen.getByRole('tooltip')).toHaveTextContent('Excluded by maintenance');
 expect(document.querySelector('.inspection-dot')).not.toBeInTheDocument();
 expect(document.querySelector('.curve')).not.toBeInTheDocument();
});

import { countExact, errorRateText } from '../src/slos/detailFormat.ts';
it('preserves exact decimal counts and small error rates without binary rounding',()=>{expect(countExact('9007199254740993.000000000001')).toBe('9,007,199,254,740,993.000000000001');expect(errorRateText('0.999')).toBe('0.1%');expect(errorRateText('0')).toBe('100%');expect(errorRateText('0.999999999999')).toBe('0.0000000001%');});

it('qualifies reduced stale coverage without connecting it as a complete interval',()=>{
 const point=sample('2026-11-09T10:00:00Z','0.8');point.display_coverage={states:['observed'],quality_states:['complete','partial'],freshness:['fresh','stale']};
 render(<SloHistoryChart kind="budget" samples={[point]} start="2026-11-06T10:00:00Z" end="2026-11-12T10:00:00Z" target="0.999"/>);const chart=screen.getByRole('group',{name:/Inspect/});chart.focus();fireEvent.keyDown(chart,{key:'Home'});expect(screen.getByRole('tooltip')).toHaveTextContent('partial');expect(screen.getByRole('tooltip')).toHaveTextContent('stale');expect(document.querySelector('.qualified-point')).toBeInTheDocument();
});

it('distinguishes repeated local times by their UTC offsets',()=>{
 vi.stubEnv('TZ','America/New_York');
 const first=pointTime('2026-11-01T05:30:00Z','local'),second=pointTime('2026-11-01T06:30:00Z','local');
 expect(first).toContain('01:30');expect(second).toContain('01:30');expect(first).toContain('GMT-4');expect(second).toContain('GMT-5');expect(first).not.toBe(second);
 expect(pointTime('2026-11-01T05:30:00Z','UTC')).toContain('05:30 UTC');
});

it('keeps dense qualified history as small marks and preserves missing gaps',()=>{
 const start='2026-10-13T10:00:00Z',end='2026-11-12T10:00:00Z';
 const samples=Array.from({length:720},(_,i)=>sample(new Date(Date.parse(start)+i*3600000).toISOString(),i===360?null:'0.8',i===360?'missing':'partial'));
 render(<SloHistoryChart kind="budget" samples={samples} start={start} end={end} target="0.999"/>);
 expect(document.querySelectorAll('circle.qualified-point')).toHaveLength(0);
 expect(document.querySelectorAll('path.qualified-point')).toHaveLength(719);
 const chart=screen.getByRole('group',{name:/Inspect/});fireEvent.keyDown(chart,{key:'Home'});
 expect(screen.getByRole('tooltip')).toHaveTextContent('80%');expect(screen.getByRole('tooltip')).toHaveTextContent('Partial data');
 expect(document.querySelectorAll('.inspection-dot')).toHaveLength(1);
});

it('explains maintenance across all lanes and treats its end as exclusive', () => {
  const start = '2026-11-06T10:00:00Z';
  const end = '2026-11-12T10:00:00Z';
  const windowEnd = '2026-11-09T10:00:00Z';
  const maintenance = [{ id: 'window-1', title: 'Checkout database upgrade', reason: 'Apply a scheduled update.', start: '2026-11-05T10:00:00Z', end: windowEnd }];
  const samples = [sample(start, '0.8'), sample(windowEnd, null, 'missing')];
  for (const kind of ['budget', 'burn', 'attainment'] as const) {
    const view = render(<SloHistoryChart kind={kind} samples={samples} start={start} end={end} target="0.999" maintenance={maintenance} />);
    const chart = screen.getByRole('group', { name: /Inspect/ });
    expect(chart.querySelector('.slo-maintenance-band')).toHaveAttribute('x', '65');
    fireEvent.keyDown(chart, { key: 'Home' });
    expect(screen.getByRole('tooltip')).toHaveTextContent('Checkout database upgrade');
    expect(screen.getByRole('tooltip')).toHaveTextContent('05 Nov 2026');
    fireEvent.keyDown(chart, { key: 'End' });
    expect(screen.getByRole('tooltip')).toHaveTextContent('Missing');
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('Checkout database upgrade');
    view.unmount();
  }
});

it('pins the selected participation only while its identity is still in the overlay', () => {
  const start = '2026-11-06T10:00:00Z';
  const end = '2026-11-06T11:00:00Z';
  const window = { id: 'first', title: 'First repair', reason: 'Replace the database.', start, end };
  const samples = [sample(start, '0.8')];
  const props = { kind: 'budget' as const, samples, start, end, target: '0.999' };
  const view = render(<SloHistoryChart {...props} maintenance={[window]} />);
  const chart = screen.getByRole('group', { name: /Inspect/ });
  fireEvent.keyDown(chart, { key: 'Home' });
  fireEvent.keyDown(chart, { key: 'Enter' });
  expect(screen.getByRole('link', { name: 'View maintenance details' })).toHaveAttribute('href', '/maintenance/first');

  view.rerender(<SloHistoryChart {...props} samples={[...samples]} maintenance={[{ ...window, title: 'First repair renamed' }]} />);
  expect(screen.getByRole('link', { name: 'View maintenance details' })).toHaveAttribute('href', '/maintenance/first');

  view.rerender(<SloHistoryChart {...props} maintenance={[{ ...window, id: 'replacement', title: 'Replacement repair' }]} />);
  expect(screen.queryByRole('group', { name: 'Maintenance inspection' })).not.toBeInTheDocument();
});

it.each([false, true])('makes overlapping maintenance keyboard-inspectable with samples present: %s', hasSamples => {
  const start = '2026-11-06T10:00:00Z';
  const end = '2026-11-06T11:00:00Z';
  const maintenance = [
    { id: 'outside', title: 'Outer repair', reason: 'Replace equipment.', start: '2026-11-06T10:10:00Z', end: '2026-11-06T10:40:00Z' },
    { id: 'inside', title: 'Inner repair', reason: 'Test the new equipment.', start: '2026-11-06T10:20:00Z', end: '2026-11-06T10:30:00Z' },
    { id: 'later', title: 'Later repair', reason: 'Check results.', start: '2026-11-06T10:45:00Z', end: '2026-11-06T10:55:00Z' },
  ];
  const samples = hasSamples ? [sample(start, '0.8'), sample(end, '0.9')] : [];
  render(<SloHistoryChart kind="budget" samples={samples} start={start} end={end} target="0.999" maintenance={maintenance} />);
  const chart = screen.getByRole('group', { name: /Inspect/ });
  expect(chart).toHaveAttribute('tabindex', '0');
  chart.focus();
  fireEvent.keyDown(chart, { key: 'ArrowDown' });
  expect(screen.getByRole('tooltip')).toHaveTextContent('Outer repair');
  fireEvent.keyDown(chart, { key: 'ArrowDown' });
  expect(screen.getByRole('tooltip')).toHaveTextContent('Inner repair');
  fireEvent.keyDown(chart, { key: 'Enter' });
  expect(screen.getByRole('link', { name: 'Outer repair' })).toHaveAttribute('href', '/maintenance/outside');
  expect(screen.getByRole('link', { name: 'Inner repair' })).toHaveAttribute('href', '/maintenance/inside');
  fireEvent.keyDown(chart, { key: 'ArrowDown' });
  expect(screen.getByRole('tooltip')).toHaveTextContent('Later repair');
});

it('shows preparation status on an empty continuation of the maintenance disclosure', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ items: [], pending: true })));
  const start = '2026-11-06T10:00:00Z';
  const end = '2026-11-06T11:00:00Z';
  const item = { id: 'first', title: 'First repair', reason: 'Replace the database.', start, end };
  const view = render(<SloMaintenanceList id="first" start={start} end={end} zone="UTC"
    page={{ items: [item], next_cursor: 'next', source: 'scoped', pending: false }} />);
  view.container.querySelector('details')!.open = true;
  fireEvent.click(screen.getByRole('button', { name: 'Next windows' }));
  expect(await screen.findByText('Some maintenance periods are still being prepared.')).toBeVisible();
});
