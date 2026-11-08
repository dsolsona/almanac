import {expect,it} from 'vitest';
import {labelsObject,labelPairs,percent,summaryMark,objectiveMark} from '../src/services/model.ts';
import {overview} from '../browser/support/service-fixtures.ts';
it('preserves exact decimal percentages and safely stores label keys',()=>{
 expect(percent('0.999999999999')).toBe('99.9999999999%');expect(percent('-0.082')).toBe('−8.2%');expect(percent(null)).toBe('—');
 const labels=labelsObject([['constructor','team'],['env','prod'],['env','canary']]);expect(labelPairs(labels)).toEqual([['constructor','team'],['env','prod'],['env','canary']]);
});
it('gives breaches precedence and never promotes incomplete or stale SLOs',()=>{
 expect(summaryMark({state:'breached',total:2,on_track:0,breached:1,unknown:1}).label).toBe('Breached');
 expect(summaryMark({state:'unknown',total:2,on_track:1,breached:0,unknown:1}).label).toBe('Unknown');
 const item=overview('00000000-0000-4000-8000-000000000001').items[0]!;
 expect(objectiveMark({...item,current_state:'stale'}).tone).toBe('stale');
 expect(objectiveMark({...item,current:{...item.current,quality:{...item.current.quality,state:'partial'}}}).tone).toBe('stale');
});
