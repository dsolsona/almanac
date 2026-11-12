import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { SloHistoryChart, pointTime } from '../src/components/charts/SloHistoryChart.tsx';
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
