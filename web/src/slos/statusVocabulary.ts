import type { components } from '../api/generated.ts';
import type { MarkTone } from '../components/ui/StatusMark.tsx';
type OverviewItem=components['schemas']['SloOperationalOverviewItem'];
type Urgency=OverviewItem['urgency'];
const reasonText:Record<string,string>={
  on_track:'On track',stale_observations:'Observations are stale',no_data:'No evaluable data',expired_history:'History has expired',
  complete_window_violation:'Complete window is below target',negative_observed_budget:'Observed budget is negative',low_observed_budget:'Less than 20% of budget remains',
  high_1h_burn:'1 h burn is at least 1×',evaluation_unavailable:'Current evaluation unavailable',not_yet_eligible:'Not yet eligible',zero_traffic:'No traffic',
  incomplete_coverage:'Coverage is incomplete',short_burn_unavailable:'Short-window burn unavailable',budget_below_half:'Less than half of budget remains',
  incomplete_or_stale_inputs:'Input coverage is incomplete or stale',cadence_mismatch:'Measurement cadence does not match the SLI',
  current_calculation_pending:'Current results are updating.',current_calculation_failed:'Current results could not be calculated.',
  source_aggregate_failed:'Measurements could not be processed. Current results are unavailable.',
};
// Short reason lines for list rows and the status line; full sentences stay in titles and the status band.
const shortReason:Record<string,string>={
  on_track:'on track',stale_observations:'observations stale',no_data:'no evaluable data',expired_history:'history expired',
  complete_window_violation:'window below target',negative_observed_budget:'observed budget negative',low_observed_budget:'under 20% of budget left',
  high_1h_burn:'1 h burn at least 1×',evaluation_unavailable:'evaluation unavailable',not_yet_eligible:'first interval pending',zero_traffic:'no traffic',
  incomplete_coverage:'coverage incomplete',short_burn_unavailable:'short burn unavailable',budget_below_half:'under half the budget left',
};
export function reasonLabel(code:string):string {return reasonText[code]??code.replaceAll('_',' ').replace(/^./,letter=>letter.toUpperCase());}
function reasonShort(code:string):string {return shortReason[code]??reasonLabel(code).toLowerCase();}
// ADR-0028 status vocabulary: derived from backend urgency and the leading reason only.
type Vocabulary={tone:MarkTone;label:string;reason:string};
export function vocabulary(urgency:Urgency,leading:OverviewItem['leading_reason'],lastKnown:boolean,lastKnownState?:string):Vocabulary {
  if(lastKnown)return {tone:'stale',label:'Stale',reason:lastKnownState==='failed'?'update failed':'calculation stale'};
  const code=leading.code,reason=reasonShort(code);
  if(urgency==='needs_attention') {
    if(code==='stale_observations')return {tone:'stale',label:'Not reporting',reason};
    if(code==='no_data'||code==='expired_history')return {tone:'stale',label:'No data',reason};
    if(code==='complete_window_violation')return {tone:'breached',label:'Breached',reason};
    if(code==='negative_observed_budget')return {tone:leading.provisional?'warning':'breached',label:'Budget exhausted',reason:leading.provisional?`${reason} · observed only`:reason};
    if(code==='low_observed_budget')return {tone:'warning',label:'Low budget',reason};
    if(code==='high_1h_burn')return {tone:'warning',label:'Burning fast',reason};
    return {tone:'warning',label:'Needs attention',reason};
  }
  if(urgency==='watch') {
    if(code==='evaluation_unavailable')return {tone:'none',label:'Unavailable',reason};
    if(code==='not_yet_eligible')return {tone:'none',label:'Waiting for data',reason};
    if(code==='zero_traffic')return {tone:'none',label:'Zero traffic',reason};
    if(code==='incomplete_coverage')return {tone:'none',label:'Partial data',reason};
    if(code==='short_burn_unavailable')return {tone:'none',label:'Burn unavailable',reason};
    if(code==='budget_below_half')return {tone:'healthy',label:'Healthy',reason};
    return {tone:'none',label:'Watch',reason};
  }
  return {tone:'healthy',label:'Healthy',reason:'on track'};
}
