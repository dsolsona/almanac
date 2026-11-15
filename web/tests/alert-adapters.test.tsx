import { expect, it } from 'vitest';
import { exactPageCount, policyBody, policyDraft, processingStatus } from '../src/alerts/adapters.ts';
import type { components } from '../src/api/generated.ts';

it('never rounds counters through floating point',()=>{
  expect(exactPageCount('200')).toBe(200);
  expect(()=>exactPageCount('9007199254740993')).toThrow('accurately');
  expect(()=>exactPageCount('01')).toThrow();
});
it('preserves exact thresholds, condition identities and a disabled policy when mapping an edit',()=>{
  const policy={name:'Burn',slo_id:'slo',severity:'medium',enabled:false,conditions:[{id:'condition',short_window_seconds:300,long_window_seconds:3600,threshold:'14.400000000000000001'}]} as components['schemas']['AlertPolicy'];
  expect(policyBody(policyDraft(policy))).toEqual({name:'Burn',slo_id:'slo',severity:'medium',confirm_non_30_day_presets:false,conditions:policy.conditions});
});
it('shows processing failure and lag separately from source evidence',()=>{
  const processing={next_boundary:'2026-11-15T12:00:00Z',schedulable:true,exhausted:false,lag_seconds:0} as components['schemas']['AlertProcessing'];
  expect(processingStatus(processing)).toEqual({state:'current'});
  expect(processingStatus({...processing,schedulable:false,waiting_reason:'policy_disabled'})).toEqual({state:'current'});
  expect(processingStatus({...processing,waiting_reason:'pending_aggregate'})).toMatchObject({state:'waiting'});
  expect(processingStatus({...processing,waiting_reason:'database_failed',exhausted:true})).toMatchObject({state:'behind',reason:'Evaluation stopped after repeated failures.'});
});
