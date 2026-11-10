import type {components} from '../../src/api/generated.ts';
type Service=components['schemas']['Service'];
type Item=components['schemas']['SloOperationalOverviewItem'];
const examples:[string,string,string,string,string[]][]=[
 ['checkout','Checkout','Accepts and processes customer orders.','Store experience',['payments','inventory','notifications']],
 ['payments','Payments','Authorizes payments and handles refunds.','Commerce',['identity']],
 ['inventory','Inventory','Tracks stock and reserves items for checkout.','Commerce',[]],
 ['storefront','Storefront','The customer-facing web store.','Store experience',['checkout','identity']],
 ['identity','Identity','Authentication and account access.','Platform',[]],
 ['notifications','Notifications','Delivers order confirmations and updates.','Platform',[]],
 ['search','Search','Finds products across the catalog.','Discovery',['inventory']],
 ['orders','Orders','Manages customer orders from placement to delivery.','Store experience',['checkout','notifications']],
 ['billing','Billing','Invoices and recurring subscriptions.','Commerce',['payments']],
 ['recommendations','Recommendations','Suggests products from recent activity.','',[]],
 ['fulfilment','Fulfilment','Coordinates picking, packing and delivery.','Operations',['orders','inventory']],
 ['legacy-reports','Legacy reports','Previous reporting service, being retired.','',[]],
];
const at='2026-11-06T13:42:00Z',start='2026-10-07T13:42:00Z';
const ids=Object.fromEntries(examples.map(([key],i)=>[key,`00000000-0000-4000-8000-${String(i+1).padStart(12,'0')}`]));
const definitions:Record<string,[string,'healthy'|'breached'|'unknown',string,number,string|null][]>= {
 checkout:[['Order completion','healthy','0.999',30,'0.624'],['Checkout latency < 500 ms','breached','0.99',30,'-0.082'],['Confirmation delivery','unknown','0.995',7,null]],
 payments:[['Payment authorization','healthy','0.9995',30,'0.841'],['Refund processing','healthy','0.999',30,'0.72']],
 inventory:[['Stock reservation','healthy','0.999',30,'0.91']],storefront:[['Store availability','healthy','0.999',30,'0.784']],identity:[['Sign-in availability','healthy','0.9995',30,'0.883']],search:[['Search latency','unknown','0.99',7,null]],orders:[['Order retrieval','healthy','0.999',30,'0.96']],billing:[['Invoice generation','healthy','0.995',30,'0.758']],fulfilment:[['Shipment creation','healthy','0.995',30,'0.614']],
};
export const services:Service[]=examples.map(([key,name,description,owner,deps])=>{
 const rows=definitions[key]??[],breached=rows.filter(row=>row[1]==='breached').length,unknown=rows.filter(row=>row[1]==='unknown').length;
 return {id:ids[key],name:key,display_name:name,description,owner,contact:owner?`${owner.toLowerCase().replaceAll(' ','-')}@example.com`:'',labels:{team:[owner?owner.toLowerCase().replaceAll(' ','-'):'unassigned'],env:['production']},links:[{name:'Runbook',url:`https://docs.example.com/${key}/runbook`},{name:'Git',url:`https://git.example.com/services/${key}`}],annotations:{},dependencies:deps.map(dep=>ids[dep]),dependency_services:[],used_by:[],used_by_count:0,slo_summary:{state:breached?'breached':unknown?'unknown':rows.length?'on_track':'no_slos',total:rows.length,on_track:rows.length-breached-unknown,breached,unknown},version:'1',created_at:'2026-10-26T09:14:00Z',updated_at:at};
});
const reference=(service:Service)=>({id:service.id,name:service.name,display_name:service.display_name,owner:service.owner??''});
for(const service of services){service.dependency_services=service.dependencies!.map(id=>reference(services.find(other=>other.id===id)!));service.used_by=services.filter(other=>other.dependencies!.includes(service.id)).map(reference);service.used_by_count=service.used_by.length;}
export const graphNames=['storefront','orders','checkout','payments','inventory','notifications','identity'];
export const graphServices=services.filter(service=>graphNames.includes(service.name));
export function servicePage(items=services,cursor=''){
 return {items:items.slice(cursor?10:0,cursor?20:10),next_cursor:!cursor&&items.length>10?'visual-next':'',total:items.length,all_count:services.length,with_slos_count:9,owners:[...new Set(services.map(service=>service.owner||'Unassigned'))].sort()};
}
export function overview(serviceID:string){
 const service=services.find(service=>service.id===serviceID)!;
 const items=(definitions[service.name]??[]).map(([name,state,target,days,budget],index):Item=>{
  const unknown=state==='unknown',freshness=service.name==='search'?'stale':'fresh';
  const quality={state:unknown&&freshness==='fresh'?'partial':'complete',freshness,expected_intervals:1,observed_intervals:unknown?0:1,known_empty_intervals:0,missing_intervals:unknown?1:0,expired_intervals:0,zero_traffic_intervals:0,coverage:unknown?'0':'1',first_observation_at:start,last_observation_at:at,latest_receipt_at:at,reason:null} as const;
  const performance={state:unknown?'unknown':state==='breached'?'violated':'met',observed_state:unknown?null:state==='breached'?'violated':'met',provisional:unknown,reason:null,good:unknown?null:'999',total:unknown?null:'1000',bad:unknown?null:'1',attainment:unknown?null:'0.999',allowed_budget:unknown?null:'100',remaining_budget:unknown?null:'62.4',consumed_fraction:unknown?null:'0.376',remaining_fraction:budget,burn:unknown?null:'0.1',budget_unit:'events'} as const;
  const current={evaluation_at:at,window:{requested_start:start,effective_start:start,end:at,eligible_start:start,retention_start:start,activated_at:start,evaluation_valid_from:start,historical_requested_by:null,historical_requested_at:null},quality,performance};
  const leading={code:unknown?'incomplete_coverage':state==='breached'?'complete_window_violation':'on_track',provisional:unknown} as const;
  return {slo:{id:`10000000-0000-4000-8000-${String(index+1).padStart(12,'0')}`,name:`objective-${index}`,display_name:name,version:'1',definition_revision:'1',service_id:service.id,service_display_name:service.display_name,sli_id:'20000000-0000-4000-8000-000000000001',sli_display_name:'Request success',target,rolling_window_seconds:days*86400,cadence_seconds:60,activated_at:start,evaluation_valid_from:start,historical_requested_by:null,historical_requested_at:null,created_at:start,updated_at:at},current,urgency:unknown?'watch':state==='breached'?'needs_attention':'healthy',leading_reason:leading,reasons:[leading],burn_1h:{window_seconds:3600,start,end:at,quality,performance},burn_6h:{window_seconds:21600,start,end:at,quality,performance},exhaustion:{state:'unavailable',remaining_events:null,bad_events_per_hour:null,hours_until_exhaustion:null,current_quality:quality,one_hour_quality:quality,assumptions:[]},budget_sparkline:[]};
 });
 return {items,next_cursor:'',previous_cursor:'',snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:items.filter(item=>item.urgency==='needs_attention').length,watch:items.filter(item=>item.urgency==='watch').length,healthy:items.filter(item=>item.urgency==='healthy').length}};
}
