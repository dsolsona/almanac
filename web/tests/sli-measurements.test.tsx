import { describe, expect, it } from 'vitest';
import { buildMeasurements, countText, measurementRatio } from '../src/slis/measurements.ts';
import type { SliObservation } from '../src/slis/client.ts';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach } from 'vitest';
import { SliMeasurementsChart } from '../src/components/charts/SliMeasurementsChart.tsx';

afterEach(cleanup);

const start = '2026-11-11T09:00:00.000Z', end = '2026-11-11T10:00:00.000Z';
function observation(index: number, changes: Partial<SliObservation> = {}): SliObservation {
  return { observation_id:`00000000-0000-4000-8000-${String(index).padStart(12, '0')}`, measurement_revision:'1', interval_start:new Date(Date.parse(start) + index * 60_000).toISOString(), interval_end:new Date(Date.parse(start) + (index + 1) * 60_000).toISOString(), outcome:'observed', good_count:'1998', total_count:'2000', error_code:null, missing_components:[], recorded_at:end, ...changes };
}
describe('SLI measurement inspection', () => {
  it('counts inspected intervals including absent observations and preserves the approved fixture arithmetic', () => {
    const rows = Array.from({ length:60 }, (_, i) => observation(i, { good_count:String(i === 59 ? 1998 : i >= 27 && i <= 31 ? [1940,1880,1850,1910,1960][i - 27] : 1998 - [0,1,0,2,0,1][i % 6]) }))
      .filter((_, i) => i !== 52).map(row => ['43','44'].some(i => row.observation_id.endsWith(i.padStart(12,'0'))) ? { ...row, outcome:'failed' as const, good_count:null, total_count:null, error_code:'timeout' as const } : row);
    const result = buildMeasurements(rows, start, end, 60);
    expect(result.intervals).toHaveLength(60);
    expect(result.counts).toEqual({ observed:57, failed:2, missing:1, partial:0, empty:0, zero:0 });
    expect(result.good).toBe('113402'); expect(result.total).toBe('114000'); expect(result.bad).toBe('598');
    expect(result.ratio).toBe('99.48'); expect(result.minimum?.ratio).toBe('92.50');
    expect(result.intervals[52]).toMatchObject({ state:'missing', good:null, total:null, ratio:null });
    expect(result.intervals[43]).toMatchObject({ state:'failed', ratio:null, detail:'Query timed out' });
  });
  it('sums fractional event counts and very large integers without floating-point loss', () => {
    const result = buildMeasurements([observation(0,{good_count:'9007199254740993.000000000001',total_count:'9007199254740994.000000000002'}),observation(1,{good_count:'0.000000000009',total_count:'0.00000000001'})], start, '2026-11-11T09:02:00Z',60);
    expect(result.good).toBe('9007199254740993.00000000001');
    expect(result.total).toBe('9007199254740994.000000000012');
    expect(result.bad).toBe('1.000000000002');
    expect(countText(result.bad)).toBe('1.000000000002');
    expect(countText(result.good)).toBe('9,007,199,254,740,993.00000000001');
  });
  it('keeps empty, partial, zero traffic and missing distinct without ratios or invented counts', () => {
    const result = buildMeasurements([observation(0,{good_count:'0.0',total_count:'0.000'}), observation(1,{outcome:'complete_empty',good_count:null,total_count:null}), observation(2,{outcome:'incomplete',good_count:null,total_count:null,missing_components:['total']})],start,'2026-11-11T09:04:00Z',60);
    expect(result.intervals.map(row => row.state)).toEqual(['zero','empty','partial','missing']);
    expect(result.intervals.every(row => row.ratio === null)).toBe(true);
    expect(result.ratio).toBeNull(); expect(result.minimum).toBeNull();
    expect(result.intervals[2]?.detail).toBe('Missing total count');
  });
  it('uses explicit inspected bounds and excludes older loaded rows from the graph summary', () => {
    const result = buildMeasurements([observation(0,{good_count:'0'}),observation(59)],'2026-11-11T09:59:00Z',end,60);
    expect(result.intervals).toHaveLength(1); expect(result.good).toBe('1998'); expect(result.ratio).toBe('99.90');
    expect(buildMeasurements([],null,null,60).intervals).toEqual([]);
    expect(buildMeasurements([],start,'2026-11-11T09:02:00Z',60).counts.missing).toBe(2);
  });
  it('rounds only the displayed percentage and does not confuse a decimal zero with traffic', () => {
    expect(measurementRatio('1997','2000')).toBe('99.85');
    expect(measurementRatio('1','3')).toBe('33.33');
    expect(measurementRatio('0','0.000')).toBeNull();
  });
  it('keeps missing shading within the plot for newly activated one- and two-slot histories', () => {
    for(const minutes of [1,2]) {
      const model=buildMeasurements([],start,new Date(Date.parse(start)+minutes*60_000).toISOString(),60);
      const {container,unmount}=render(<SliMeasurementsChart intervals={model.intervals}/>);
      for(const gap of container.querySelectorAll('.gap')) {
        const left=Number(gap.getAttribute('x')),width=Number(gap.getAttribute('width'));
        expect(left).toBeGreaterThanOrEqual(48);
        expect(left+width).toBeLessThanOrEqual(950);
        if(minutes===1)expect(width).toBe(902);
      }
      unmount();
    }
  });
  it('inspects exact observations and gaps with keyboard boundaries, pointer selection and dismissal', () => {
    const model=buildMeasurements([observation(0,{good_count:'1850'}),observation(2,{outcome:'failed',good_count:null,total_count:null,error_code:'timeout'})],start,'2026-11-11T09:03:00Z',60);
    const {container}=render(<SliMeasurementsChart intervals={model.intervals}/>);
    const chart=screen.getByRole('group',{name:/Interactive measurements/});
    fireEvent.focus(chart);fireEvent.keyDown(chart,{key:'Home'});
    expect(screen.getByRole('tooltip')).toHaveTextContent('92.50%');
    expect(screen.getByRole('tooltip')).toHaveTextContent('1,850');
    fireEvent.keyDown(chart,{key:'ArrowLeft'});
    expect(screen.getByRole('tooltip')).toHaveTextContent('09:00–09:01 UTC');
    fireEvent.keyDown(chart,{key:'ArrowRight'});
    expect(screen.getByRole('tooltip')).toHaveTextContent('No observation received');
    expect(container.querySelector('.active-dot')).toBeNull();
    fireEvent.keyDown(chart,{key:'End'});fireEvent.keyDown(chart,{key:'ArrowRight'});
    expect(screen.getByRole('tooltip')).toHaveTextContent('Query timed out');
    fireEvent.keyDown(chart,{key:'Escape'});
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
    fireEvent.pointerMove(container.querySelector('[data-point="0"]')!);
    expect(screen.getByRole('tooltip')).toHaveTextContent('92.50%');
    fireEvent.pointerLeave(chart);
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  });
});
