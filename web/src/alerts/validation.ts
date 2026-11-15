// Validate saved server evidence; never infer alert truth or recompute burn here.
export const object=(v:unknown):v is Record<string,unknown>=>typeof v==='object'&&v!==null&&!Array.isArray(v);
export const text=(v:unknown):v is string=>typeof v==='string';
export const uuid=(v:unknown):v is string=>text(v)&&/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v)&&v!=='00000000-0000-0000-0000-000000000000';
export const counter=(v:unknown,positive=false):v is string=>text(v)&&/^(0|[1-9][0-9]*)$/.test(v)&&(!positive||v!=='0')&&(v.length<19||v.length===19&&v<='9223372036854775807');
export const decimal=(v:unknown):v is string=>text(v)&&v.length<=96&&/^[0-9]+(?:\.[0-9]+)?$/.test(v);
const threshold=(v:unknown)=>decimal(v)&&v.length<=32&&/[1-9]/.test(v);
export const timestamp=(v:unknown):v is string=>text(v)&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(v)&&Number.isFinite(Date.parse(v));
const integer=(v:unknown,min=0):v is number=>typeof v==='number'&&Number.isSafeInteger(v)&&v>=min;
const member=(v:unknown,values:string[])=>text(v)&&values.includes(v);
const optional=(v:unknown,check:(v:unknown)=>boolean)=>v===undefined||check(v);
const array=(v:unknown,check:(v:unknown)=>boolean,max=200,min=0):v is unknown[]=>Array.isArray(v)&&v.length>=min&&v.length<=max&&v.every(check);
const unique=(values:unknown[],key:string)=>new Set(values.map(v=>(v as Record<string,unknown>)[key])).size===values.length;
const severity=(v:unknown)=>member(v,['low','medium','high']);
const lifecycle=(v:unknown)=>member(v,['firing','acknowledged','resolved','disabled','superseded']);
const truth=(v:unknown)=>member(v,['breaching','clear','indeterminate']);
const quality=(v:unknown)=>member(v,['complete','missing','partial','stale','zero_traffic','warmup','expired','excluded','calculation_failed']);
const outcome=(v:unknown)=>member(v,['matching','not_matching','unavailable']);
const duration=(v:unknown)=>integer(v,60)&&(v as number)<=2592000&&(v as number)%60===0;
const conditionID=(v:unknown)=>text(v)&&v.length>0&&v.length<=64&&/^[\x20-\x7e]+$/.test(v)&&v.trim().length>0;
const condition=(v:unknown)=>object(v)&&conditionID(v.id)&&duration(v.short_window_seconds)&&duration(v.long_window_seconds)&&(v.short_window_seconds as number)<=(v.long_window_seconds as number)&&threshold(v.threshold);
export const policy=(v:unknown):boolean=>object(v)&&uuid(v.id)&&text(v.name)&&v.name.trim().length>0&&uuid(v.slo_id)&&severity(v.severity)&&typeof v.enabled==='boolean'&&counter(v.version,true)&&counter(v.evaluation_revision,true)&&array(v.conditions,condition,8,1)&&unique(v.conditions,'id')&&timestamp(v.created_at)&&timestamp(v.updated_at);
const snapshotKeys=['slo_id','slo_revision','sli_revision','source_generation','exclusion_revision','calculation_version','cutoff'];
const snapshot=(v:unknown):v is Record<string,unknown>=>object(v)&&uuid(v.slo_id)&&counter(v.slo_revision,true)&&counter(v.sli_revision,true)&&counter(v.source_generation)&&counter(v.exclusion_revision)&&text(v.calculation_version)&&v.calculation_version.length>0&&timestamp(v.cutoff);
const sameSnapshot=(a:Record<string,unknown>,b:Record<string,unknown>)=>snapshotKeys.every(key=>a[key]===b[key]);
const sameBinding=(a:Record<string,unknown>,b:Record<string,unknown>)=>['slo_id','slo_revision','sli_revision','calculation_version'].every(key=>a[key]===b[key]);
function window(v:unknown,identity:Record<string,unknown>):boolean{
 if(!object(v)||!snapshot(v.snapshot)||!sameSnapshot(v.snapshot,identity)||!timestamp(v.start)||!timestamp(v.end)||v.end!==identity.cutoff||Date.parse(v.start)>=Date.parse(v.end)||!quality(v.quality)||!optional(v.reason,text))return false;
 if((v.good===undefined)!==(v.total===undefined))return false;
 return ['good','total','bad','observed_error_rate','allowed_error_rate','burn'].every(k=>optional(v[k],decimal));
}
function conditions(v:unknown,identity:Record<string,unknown>):boolean{
 return array(v,item=>object(item)&&conditionID(item.id)&&threshold(item.threshold)&&outcome(item.outcome)&&window(item.short,identity)&&window(item.long,identity),8,1)&&unique(v,'id');
}
function comparison(v:unknown):boolean{return object(v)&&snapshot(v.snapshot)&&truth(v.truth)&&conditions(v.conditions,v.snapshot);}
const evaluation=(v:unknown):v is Record<string,unknown>=>object(v)&&uuid(v.policy_id)&&counter(v.evaluation_revision,true)&&comparison(v)&&optional(v.recovery_comparison,comparison);
const objective=(v:unknown)=>object(v)&&decimal(v.target)&&integer(v.cadence_seconds,1)&&timestamp(v.definition_valid_from)&&timestamp(v.measurement_valid_from)&&timestamp(v.retention_start);
export function occurrence(v:unknown):v is Record<string,unknown>{
 if(!object(v)||!uuid(v.id)||!uuid(v.policy_id)||!counter(v.evaluation_revision,true)||!lifecycle(v.state)||!counter(v.version,true)||!counter(v.sequence,true)||!snapshot(v.firing_snapshot)||!snapshot(v.latest_snapshot)||!snapshot(v.latest_breaching_snapshot)||!sameBinding(v.firing_snapshot,v.latest_snapshot)||!sameBinding(v.firing_snapshot,v.latest_breaching_snapshot)||!timestamp(v.opened_detected_at)||!timestamp(v.updated_at)||!optional(v.closed_at,timestamp)||!optional(v.acknowledged_by,uuid)||!optional(v.acknowledged_at,timestamp))return false;
 if((v.acknowledged_by===undefined)!==(v.acknowledged_at===undefined))return false;
 if(member(v.state,['resolved','disabled','superseded'])!==(v.closed_at!==undefined)||v.state==='acknowledged'&&v.acknowledged_by===undefined)return false;
 const p=v.original_policy;return object(p)&&text(p.name)&&severity(p.severity)&&text(p.slo_name)&&text(p.service_name)&&decimal(p.target)&&integer(p.rolling_window_seconds,1);
}
export const evidence=(v:unknown):boolean=>object(v)&&uuid(v.id)&&timestamp(v.scheduled_at)&&timestamp(v.committed_at)&&evaluation(v.evaluation)&&objective(v.objective);
export const processing=(v:unknown):v is Record<string,unknown>=>object(v)&&uuid(v.policy_id)&&counter(v.revision,true)&&typeof v.schedulable==='boolean'&&typeof v.exhausted==='boolean'&&timestamp(v.next_boundary)&&timestamp(v.available_at)&&optional(v.pending_cutoff,timestamp)&&optional(v.last_cutoff,timestamp)&&optional(v.last_evaluation_id,uuid)&&optional(v.waiting_reason,text)&&integer(v.failure_attempts)&&integer(v.lag_seconds);
const cursor=(v:unknown)=>optional(v,x=>text(x)&&x.length<=1024);
function page(v:unknown,check:(v:unknown)=>boolean,count=true):v is Record<string,unknown>{return object(v)&&array(v.items,check)&&(!count||counter(v.total))&&cursor(v.next_cursor);}
export const policies=(v:unknown):boolean=>page(v,item=>object(item)&&policy(item.policy)&&text(item.slo_name)&&optional(item.open_occurrence_id,uuid)&&processing(item.processing)&&item.processing.policy_id===(item.policy as Record<string,unknown>).id&&item.processing.revision===(item.policy as Record<string,unknown>).evaluation_revision);
function catalogItem(v:unknown):boolean{
 if(!object(v)||!occurrence(v.alert)||!text(v.policy_name)||!severity(v.severity)||!uuid(v.slo_id)||!text(v.slo_name)||!uuid(v.service_id)||!text(v.service_name)||!uuid(v.evaluation_id)||!evaluation(v.evaluation)||!timestamp(v.scheduled_at)||!timestamp(v.committed_at)||!processing(v.processing))return false;
 return v.alert.policy_id===v.evaluation.policy_id&&v.alert.evaluation_revision===v.evaluation.evaluation_revision&&v.alert.policy_id===v.processing.policy_id&&v.alert.evaluation_revision===v.processing.revision&&v.slo_id===(v.evaluation.snapshot as Record<string,unknown>).slo_id&&sameSnapshot(v.alert.latest_snapshot as Record<string,unknown>,v.evaluation.snapshot as Record<string,unknown>);
}
export const list=(v:unknown):boolean=>page(v,catalogItem)&&array(v.groups,g=>object(g)&&uuid(g.slo_id)&&text(g.slo_name)&&counter(g.total)&&counter(g.firing)&&BigInt(g.firing)<=BigInt(g.total))&&unique(v.groups,'slo_id');
export const operations=['alert-policy.create','alert-policy.update','alert-policy.enable','alert-policy.disable','alert-policy.retry','alert.acknowledge'];
export const requestID=(v:unknown):v is string=>text(v)&&/^[A-Za-z0-9._:-]{1,128}$/.test(v);
export function command(v:unknown,operation?:string,id?:string):v is Record<string,unknown>{
 if(!object(v)||!member(v.operation,operations)||operation!==undefined&&v.operation!==operation||!requestID(v.request_id))return false;
 if(v.operation==='alert.acknowledge')return v.policy===undefined&&occurrence(v.alert)&&(id===undefined||v.alert.id===id);
 if(!policy(v.policy)||!optional(v.alert,occurrence))return false;
 const p=v.policy as Record<string,unknown>;return (id===undefined||p.id===id)&&(v.alert===undefined||(v.alert as Record<string,unknown>).policy_id===p.id);
}
export const lookup=(v:unknown,operation:string):boolean=>object(v)&&member(v.status,['committed','expired','unknown'])&&(v.status==='committed'?command(v.result,operation):v.result===undefined);
export const transitions=(v:unknown):boolean=>page(v,item=>object(item)&&uuid(item.id)&&optional(item.from,lifecycle)&&lifecycle(item.to)&&member(item.cause,['condition_matched','conditions_cleared','resolved_after_maintenance_change','evidence_updated','acknowledged','policy_disabled','definition_superseded','slo_archived'])&&optional(item.actor_id,uuid)&&optional(item.reason,text)&&optional(item.evaluation_id,uuid)&&text(item.request_id)&&counter(item.sequence,true)&&timestamp(item.effective_at)&&timestamp(item.detected_at),false);
function summary(v:unknown):boolean{return object(v)&&snapshot(v.first)&&snapshot(v.last)&&sameBinding(v.first,v.last)&&truth(v.truth)&&array(v.conditions,item=>object(item)&&conditionID(item.id)&&threshold(item.threshold)&&outcome(item.outcome)&&duration(item.short_window_seconds)&&duration(item.long_window_seconds)&&quality(item.short_quality)&&quality(item.long_quality)&&optional(item.short_reason,text)&&optional(item.long_reason,text),8,1)&&unique(v.conditions,'id');}
export const ranges=(v:unknown):boolean=>page(v,item=>object(item)&&uuid(item.id)&&member(item.kind,['repeated_cutoff','unreconstructed_gap','administrative_cancellation','compacted_evaluations'])&&text(item.reason)&&optional(item.actor_id,uuid)&&optional(item.request_id,text)&&optional(item.evaluation_id,uuid)&&timestamp(item.start)&&timestamp(item.end)&&Date.parse(item.start)<Date.parse(item.end)&&timestamp(item.recorded_at)&&integer(item.cadence_seconds,1)&&optional(item.summary,summary)&&(item.kind==='compacted_evaluations'?item.summary!==undefined:item.summary===undefined),false);

export function series(v:unknown,conditionID:string):boolean {
 if(!object(v)||!uuid(v.policy_id)||!counter(v.evaluation_revision,true)||!objective(v.objective)||!condition(v.condition)||(v.condition as Record<string,unknown>).id!==conditionID||!timestamp(v.start)||!timestamp(v.end)||Date.parse(v.start)>Date.parse(v.end)||Date.parse(v.end)-Date.parse(v.start)>86400000||!object(v.sampling))return false;
 const sample=v.sampling;
 if(!integer(sample.total)||sample.total>1441||!integer(sample.returned)||sample.returned>120||sample.returned>sample.total||typeof sample.reduced!=='boolean'||sample.reduced!==(sample.returned<sample.total)||typeof sample.omitted_extrema!=='boolean'||!member(sample.method,['exact','extrema_priority']))return false;
 let previous='';
 if(!array(v.points,p=>{
  if(!object(p)||!uuid(p.evaluation_id)||!snapshot(p.snapshot)||!timestamp(p.scheduled_at)||!timestamp(p.committed_at)||!conditions([p.condition],p.snapshot)||(p.condition as Record<string,unknown>).id!==conditionID)return false;
  const cutoff=p.snapshot.cutoff as string;
  // ISO timestamps may use differing fractional scales. Compare the exact UTC instant.
  const stamp=(value:string)=>value.replace(/(?:\.(\d+))?Z$/,(_,fraction:string|undefined)=>'.'+(fraction??'').padEnd(9,'0')+'Z');
  if(stamp(cutoff)<stamp(v.start as string)||stamp(cutoff)>stamp(v.end as string)||previous&&stamp(cutoff)<=stamp(previous))return false;
  previous=cutoff;return true;
 },120)||v.points.length!==sample.returned||!unique(v.points,'evaluation_id'))return false;
 return array(v.gaps,g=>object(g)&&timestamp(g.start)&&timestamp(g.end)&&Date.parse(g.start)<=Date.parse(g.end)&&typeof g.start_inclusive==='boolean'&&typeof g.end_inclusive==='boolean'&&member(g.basis,['effective_cutoff','scheduled_boundary'])&&member(g.kind,['not_retained','unavailable','repeated_cutoff','unreconstructed_gap','administrative_cancellation','compacted_evaluations','outside_revision','pending'])&&text(g.reason)&&optional(g.range_id,uuid),4400);
}
