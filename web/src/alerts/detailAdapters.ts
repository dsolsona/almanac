import type { components } from '../api/generated.ts';
import type { TimelineChart, TimelinePoint } from '../components/charts/timelineModel.ts';
import type { AlertConditionEvidence, AlertEvidenceSnapshot, AlertOccurrenceDetailData } from './AlertOccurrenceDetail.tsx';
import { conditionName, processingStatus } from './adapters.ts';

type Schema=components['schemas'];
export const outcomeExplanation=(outcome:Schema['AlertConditionOutcome'])=>outcome==='matching'?'Both burn windows meet the threshold.':outcome==='not_matching'?'At least one burn window is below the threshold.':'This condition has insufficient usable evidence. Its saved outcome is unavailable.';
export function evidenceChart(series:Schema['AlertSeries']|undefined,condition:Schema['AlertConditionResult']):TimelineChart {
  const end=series?.end??condition.short.end,start=series?.start??new Date(Date.parse(end)-7200000).toISOString();
  const points=(which:'short'|'long'):TimelinePoint[]=>{
    if(!series)return [];
    const result:TimelinePoint[]=[];
    for(const [index,point]of series.points.entries()){
      const previous=series.points[index-1];
      const time=Date.parse(point.snapshot.cutoff);
      if(previous){
        const prior=Date.parse(previous.snapshot.cutoff);
        if(time<=prior)return []; // The chart cannot represent sub-millisecond cutoff separation.
        const gap=series.gaps.find(gap=>gap.basis==='effective_cutoff'&&gap.start!==gap.end&&Date.parse(gap.start)<time&&Date.parse(gap.end)>prior);
        if(gap||series.sampling.reduced){
          const middle=Math.floor((prior+time)/2);
          if(middle>prior&&middle<time)result.push({timestamp:new Date(middle).toISOString(),value:null,coverage:'missing',reason:gap?'Retained evaluation evidence is unavailable in this interval.':'Intermediate evaluations omitted by chart sampling.'});
        }
      }
      const window=point.condition[which],value=window.burn??null;
      const coverage=window.quality==='complete'&&value!==null?'complete':window.quality==='partial'?'partial':window.quality==='stale'&&value!==null?'stale':window.quality==='zero_traffic'?'zero-traffic':window.quality==='expired'?'expired':'missing';
      result.push({timestamp:point.snapshot.cutoff,value:['complete','partial','stale'].includes(coverage)?value:null,coverage,reason:window.reason??(window.quality!=='complete'?window.quality.replaceAll('_',' '):undefined)});
    }
    return result;
  };
  return {title:'Burn rate',summary:series?`${series.sampling.returned} retained evaluations${series.sampling.reduced?` of ${series.sampling.total}; intermediate evaluations omitted`:''}${series.sampling.omitted_extrema?'; some extrema omitted':''}.`:'Retained chart history is unavailable. Saved evidence is shown below.',start,end,stepMs:60000,fillExpectedGaps:false,timeZone:'UTC',
    lanes:[{id:'burn',label:'Burn rate',unit:'×',series:[{id:'short',label:'Short window',color:'primary',points:points('short')},{id:'long',label:'Long window',points:points('long')}]}],referenceLines:[{lane:'burn',value:condition.threshold,label:`${condition.threshold}× threshold`}]};
}
export function evidenceSnapshot(value:Schema['AlertEvidence'],occurrence:Schema['AlertRecord'],series:Map<string,Schema['AlertSeries']>):AlertEvidenceSnapshot {
  const evaluation=value.evaluation,identity=evaluation.snapshot;
  if(evaluation.policy_id!==occurrence.policy_id||evaluation.evaluation_revision!==occurrence.evaluation_revision)throw new Error('Evidence belongs to a different policy revision. Refresh the alert.');
  return {evaluatedAt:identity.cutoff,sloTarget:value.objective.target,sloWindowSeconds:occurrence.original_policy.rolling_window_seconds,policyRevision:evaluation.evaluation_revision,sloRevision:identity.slo_revision,measurementRevision:identity.sli_revision,sourceGeneration:identity.source_generation,exclusionRevision:identity.exclusion_revision,calculationVersion:identity.calculation_version,
    primaryConditionId:(evaluation.conditions.find(condition=>condition.outcome==='matching')??evaluation.conditions[0])!.id,
    conditions:evaluation.conditions.map(condition=>{
      function window(which:'short'|'long'){
        const item=condition[which];
        return {label:which==='short'?'Short window':'Long window',seconds:(Date.parse(item.end)-Date.parse(item.start))/1000,start:item.start,end:item.end,bad:item.bad??null,total:item.total??null,observedErrorRate:item.observed_error_rate??null,allowedErrorRate:item.allowed_error_rate??null,burn:item.burn??null,reason:item.reason??item.quality.replaceAll('_',' '),quality:item.quality};
      }
      return {id:condition.id,name:conditionName(condition),outcome:condition.outcome,explanation:outcomeExplanation(condition.outcome),threshold:condition.threshold,windows:[window('short'),window('long')],chart:evidenceChart(series.get(condition.id),condition)} satisfies AlertConditionEvidence;
    })};
}
const lifecycleLabels:Record<Schema['AlertLifecycle'],string>={firing:'Firing',acknowledged:'Acknowledged',resolved:'Resolved',disabled:'Disabled',superseded:'Superseded'};
export function occurrenceDetail(record:Schema['AlertRecord'],original:Schema['AlertEvidence'],latest:Schema['AlertEvidence'],processing:Schema['AlertProcessing'],transitions:Schema['AlertTransitionPage'],series:Map<string,Schema['AlertSeries']>,userID?:string):AlertOccurrenceDetailData {
  if(latest.evaluation.snapshot.cutoff!==record.latest_snapshot.cutoff||original.evaluation.snapshot.cutoff!==record.firing_snapshot.cutoff)throw new Error('The alert changed while evidence was being read. Refresh to see its current evidence.');
  const last=[...transitions.items].reverse().find(item=>['resolved','disabled','superseded'].includes(item.to));
  const progress=processing.revision===record.evaluation_revision?processingStatus(processing):{state:'current' as const};
  const stateReason=record.state==='firing'?'A condition matched. The alert remains open until complete evidence confirms recovery.':record.state==='acknowledged'?'Someone is handling this alert. It remains open until complete evidence confirms recovery.':record.state==='resolved'?'Complete evidence confirmed recovery.':record.state==='disabled'?'This alert was closed when its policy or SLO was disabled.':'A new policy definition superseded this alert.';
  return {id:record.id,version:record.version,policyName:record.original_policy.name,slo:{id:record.firing_snapshot.slo_id,name:record.original_policy.slo_name,service:record.original_policy.service_name,href:`/slos/${record.firing_snapshot.slo_id}`},state:record.state,severity:record.original_policy.severity,reason:stateReason,openedAt:record.firing_snapshot.cutoff,detectedAt:record.opened_detected_at,closedAt:record.closed_at,terminalCause:last?.reason??last?.cause.replaceAll('_',' '),
    ...(record.acknowledged_at&&record.acknowledged_by?{acknowledged:{at:record.acknowledged_at,actor:record.acknowledged_by===userID?'You':record.acknowledged_by}}:{}),
    processing:progress.state==='current'?progress:{...progress,totalDelaySeconds:processing.lag_seconds},
    original:evidenceSnapshot(original,record,new Map()),latest:evidenceSnapshot(latest,record,series),
    lifecycle:transitions.items.map(item=>({id:item.id,at:item.detected_at,effectiveAt:item.effective_at,title:lifecycleLabels[item.to],description:item.reason??item.cause.replaceAll('_',' '),actor:item.actor_id?(item.actor_id===userID?'You':item.actor_id):undefined}))};
}
