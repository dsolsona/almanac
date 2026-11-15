import { expect, it } from 'vitest';
import type { components } from '../src/api/generated.ts';
import { evidenceChart } from '../src/alerts/detailAdapters.ts';
import { series as validSeries } from '../src/alerts/validation.ts';
import { validateTimeline } from '../src/components/charts/timelineModel.ts';
type Schema=components['schemas'];
const policy='11111111-1111-4111-8111-111111111111',slo='22222222-2222-4222-8222-222222222222';
function point(minute:number,burn='14.4'):Schema['AlertSeriesPoint']{
  const at=`2026-11-15T12:${String(minute).padStart(2,'0')}:00Z`,snapshot={slo_id:slo,slo_revision:'1',sli_revision:'1',source_generation:'9007199254740993',exclusion_revision:'0',calculation_version:'occurrences-v1',cutoff:at};
  const window={snapshot,start:new Date(Date.parse(at)-300000).toISOString(),end:at,quality:'complete' as const,good:'9856',total:'10000',bad:'144',observed_error_rate:'0.0144',allowed_error_rate:'0.001',burn};
  return {evaluation_id:`33333333-3333-4333-8333-${String(minute+1).padStart(12,'0')}`,snapshot,scheduled_at:at,committed_at:at,condition:{id:'fast',threshold:'14.4',outcome:'matching',short:window,long:{...window,start:new Date(Date.parse(at)-3600000).toISOString()}}};
}
const first=point(0),last=point(2);
const value:Schema['AlertSeries']={policy_id:policy,evaluation_revision:'1',objective:{target:'0.999',cadence_seconds:60,definition_valid_from:'2026-10-01T00:00:00Z',measurement_valid_from:'2026-10-01T00:00:00Z',retention_start:'2026-10-01T00:00:00Z'},condition:{id:'fast',threshold:'14.4',short_window_seconds:300,long_window_seconds:3600},start:first.snapshot.cutoff,end:last.snapshot.cutoff,points:[first,last],gaps:[],sampling:{total:2,returned:2,reduced:false,omitted_extrema:false,method:'exact'}};

it('preserves exact saved rates and end-aligned paired points without browser SLO calculations',()=>{
  expect(validSeries(value,'fast')).toBe(true);
  const chart=evidenceChart(value,first.condition);expect(chart.fillExpectedGaps).toBe(false);
  expect(chart.lanes[0]!.series[0]!.points.map(point=>point.value)).toEqual(['14.4','14.4']);
  expect(validateTimeline(chart).series[0]!.data).toHaveLength(2);
});
it('interrupts cutoff gaps and sampled intervals while retaining actual endpoints',()=>{
  for(const extra of [
    {gaps:[{start:first.snapshot.cutoff,end:last.snapshot.cutoff,start_inclusive:false,end_inclusive:false,basis:'effective_cutoff' as const,kind:'not_retained' as const,reason:'no_retained_evaluation'}]},
    {sampling:{total:3,returned:2,reduced:true,omitted_extrema:true,method:'extrema_priority' as const}},
  ]){
    const chart=evidenceChart({...value,...extra},first.condition),points=chart.lanes[0]!.series[0]!.points;
    expect(points.map(point=>point.value)).toEqual(['14.4',null,'14.4']);validateTimeline(chart);
  }
});
it('does not draw a scheduled range as an effective-cutoff gap',()=>{
  for(const kind of ['repeated_cutoff','before_policy_activation'] as const){
    const data={...value,gaps:[{start:first.snapshot.cutoff,end:last.snapshot.cutoff,start_inclusive:true,end_inclusive:false,basis:'scheduled_boundary' as const,kind,reason:kind}]};
    expect(validSeries(data,'fast')).toBe(true);
    const chart=evidenceChart(data,first.condition);
    expect(chart.lanes[0]!.series[0]!.points).toHaveLength(2);
    if(kind==='before_policy_activation')expect(validSeries({...data,gaps:[{...data.gaps[0]!,basis:'effective_cutoff'}]},'fast')).toBe(false);
  }
});
it('keeps partial numerics provisional and zero traffic unavailable',()=>{
  const partial=structuredClone(first);partial.condition.short.quality='partial';partial.condition.outcome='unavailable';
  const zero=structuredClone(last);zero.condition.short.quality='zero_traffic';zero.condition.short.burn=undefined;
  const chart=evidenceChart({...value,points:[partial,zero]},first.condition);
  expect(chart.lanes[0]!.series[0]!.points.map(point=>[point.value,point.coverage])).toEqual([['14.4','partial'],[null,'zero-traffic']]);validateTimeline(chart);
});
it('rejects wrong condition, duplicate cutoff, dishonest sampling and one-sided counts',()=>{
  expect(validSeries(value,'other')).toBe(false);
  expect(validSeries({...value,points:[first,first]},'fast')).toBe(false);
  expect(validSeries({...value,sampling:{...value.sampling,returned:1}},'fast')).toBe(false);
  const broken=structuredClone(value);broken.points[0]!.condition.short.total=undefined;
  expect(validSeries(broken,'fast')).toBe(false);
});
