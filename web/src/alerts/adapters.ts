import type { components } from '../api/generated.ts';
import type { AlertListPage, AlertOccurrenceRow } from './AlertOccurrenceList.tsx';
import type { AlertPolicyDraft } from './policyDraft.ts';
import { conditionLabels, conditionPreset } from './policyDraft.ts';

type Schema = components['schemas'];
export const conditionName = (value: Schema['AlertConditionResult']) => conditionLabels[conditionPreset({
  threshold: value.threshold,
  shortSeconds: (Date.parse(value.short.end) - Date.parse(value.short.start)) / 1000,
  longSeconds: (Date.parse(value.long.end) - Date.parse(value.long.start)) / 1000,
})];
export function exactPageCount(value: string): number {
  if (!/^(0|[1-9]\d*)$/.test(value) || BigInt(value) > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('The result count cannot be displayed accurately. Narrow the filters.');
  return Number(value);
}
export const policyDraft = (value: Schema['AlertPolicy']): AlertPolicyDraft => ({
  name: value.name, sloId: value.slo_id, severity: value.severity, confirmNonThirtyDayPresets: false,
  conditions: value.conditions.map(condition => ({id: condition.id, threshold: condition.threshold, shortSeconds: condition.short_window_seconds, longSeconds: condition.long_window_seconds})),
});
export const policyBody = (draft: AlertPolicyDraft) => ({
  name: draft.name, slo_id: draft.sloId, severity: draft.severity,
  confirm_non_30_day_presets: draft.confirmNonThirtyDayPresets,
  conditions: draft.conditions.map(value => ({id: value.id, threshold: value.threshold, short_window_seconds: value.shortSeconds, long_window_seconds: value.longSeconds})),
});
export function processingStatus(value: Schema['AlertProcessing']): AlertOccurrenceRow['processing'] {
  if (value.exhausted) return {state:'behind', dueAt:value.next_boundary, reason:'Evaluation stopped after repeated failures.'};
  if (value.schedulable && value.waiting_reason) return {state:'waiting', dueAt:value.next_boundary, reason:'Waiting for calculation: ' + value.waiting_reason.replaceAll('_',' ')};
  if (value.schedulable && value.lag_seconds > 0) return {state:'behind', dueAt:value.next_boundary, reason:'Evaluation is behind schedule.'};
  return {state:'current'};
}
export function occurrenceRow(item: Schema['AlertCatalogItem'], userID?: string): AlertOccurrenceRow {
  const value = item.alert;
  return {
    id:value.id, version:value.version, policyName:item.policy_name, href:`/alerts/${value.id}`,
    slo:{id:item.slo_id,name:item.slo_name,service:item.service_name,href:`/slos/${item.slo_id}`},
    state:value.state, severity:item.severity, openedAt:value.firing_snapshot.cutoff, detectedAt:value.opened_detected_at,
    ...(value.closed_at ? {closedAt:value.closed_at}:{}),
    ...(value.acknowledged_at && value.acknowledged_by ? {acknowledged:{at:value.acknowledged_at,actor:value.acknowledged_by === userID ? 'You' : value.acknowledged_by}}:{}),
    evidence:{at:item.evaluation.snapshot.cutoff, ...(item.evaluation.truth==='clear'?{explanation:item.evaluation.recovery_comparison?.truth==='indeterminate'?'Current conditions are clear; recovery evidence under the prior maintenance definition is unavailable.':item.evaluation.recovery_comparison?.truth==='breaching'?'Current conditions are clear; recovery still breaches under the prior maintenance definition.':'All conditions are clear.'}:{}), matches:item.evaluation.conditions.filter(condition => condition.outcome === 'matching').map(condition => {
      if (condition.short.burn === undefined || condition.long.burn === undefined) throw new Error('Matching alert evidence is incomplete. Refresh to try again.');
      return {id:condition.id,name:conditionName(condition),threshold:condition.threshold,shortBurn:condition.short.burn,longBurn:condition.long.burn,
        shortSeconds:(Date.parse(condition.short.end)-Date.parse(condition.short.start))/1000,longSeconds:(Date.parse(condition.long.end)-Date.parse(condition.long.start))/1000};
    }), ...(item.evaluation.truth === 'indeterminate' ? {unavailableReason:'Recovery evidence is unavailable; the committed alert state is preserved.'}:{})},
    processing:processingStatus(item.processing),
  };
}
export function occurrencePage(value: Schema['AlertPage'], grouped: boolean, previousPages: number, size: number, userID?: string): AlertListPage {
  const rows = value.items.map(item => occurrenceRow(item,userID)), total = exactPageCount(value.total);
  const bounds = {total,start:rows.length ? previousPages*size+1 : 0,end:previousPages*size+rows.length,hasPrevious:previousPages>0,hasNext:!!value.next_cursor};
  if (!grouped) return {...bounds,kind:'flat',items:rows};
  return {...bounds,kind:'slo',groups:value.groups.flatMap(group => {
    const items=rows.filter(row=>row.slo.id===group.slo_id);
    return items.length ? [{slo:items[0]!.slo,total:exactPageCount(group.total),firing:exactPageCount(group.firing),items}] : [];
  })};
}
