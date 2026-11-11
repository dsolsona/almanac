import type { Sli, SliObservation, SliObservationPage } from '../../src/slis/client.ts';
import type { Page } from '@playwright/test';
import { overview, services } from './service-fixtures.ts';
import type { CurrentStatus } from '../../src/slos/client.ts';

export const sliID='20000000-0000-4000-8000-000000000001';
export const sourceID='30000000-0000-4000-8000-000000000001';
export const agentID='40000000-0000-4000-8000-000000000001';
export const start='2026-11-11T09:00:00Z',end='2026-11-11T10:00:00Z';
export const observations:SliObservation[]=Array.from({length:60},(_,i):SliObservation=>{
  const outcome=i===43||i===44?'failed':'observed';
  const good=i===59?1998:i>=27&&i<=31?[1940,1880,1850,1910,1960][i-27]!:1998-[0,1,0,2,0,1][i%6]!;
  return {observation_id:`50000000-0000-4000-8000-${String(i+1).padStart(12,'0')}`,measurement_revision:'1',interval_start:new Date(Date.parse(start)+i*60_000).toISOString(),interval_end:new Date(Date.parse(start)+(i+1)*60_000).toISOString(),outcome,good_count:outcome==='observed'?String(good):null,total_count:outcome==='observed'?'2000':null,error_code:outcome==='failed'?'timeout':null,missing_components:[],recorded_at:end};
}).filter((_,i)=>i!==52).reverse();
export const sli:Sli={id:sliID,name:'checkout-request-success',display_name:'Checkout request success',version:'1',measurement_revision:'1',datasource_id:sourceID,datasource_version:'1',agent_id:agentID,agent_reachability:'connected',collection_progress:'running',measurement_kind:'event_counts',component_mode:'interval_count',good_query:'sum(increase(http_requests_total{service="checkout",status!~"5.."}[1m]))',total_query:'sum(increase(http_requests_total{service="checkout"}[1m]))',polling_interval_seconds:60,settling_delay_seconds:60,effective_from:'2026-11-01T00:00:00Z',activated_at:'2026-11-01T00:00:00Z',activation_state:'active',freshness:'fresh',latest_observation:observations[0]!,last_attempt_at:'2026-11-11T10:01:20Z',last_observation_at:'2026-11-11T10:01:20Z',next_expected_execution_at:'2026-11-11T10:02:20Z',created_at:'2026-11-01T00:00:00Z',updated_at:'2026-11-01T00:00:00Z',datasource_display_name:'Production Prometheus',recent_measurements:[],linked_slos:{items:[{id:'10000000-0000-4000-8000-000000000001',name:'checkout-availability',display_name:'Checkout availability'},{id:'10000000-0000-4000-8000-000000000002',name:'checkout-weekly',display_name:'Checkout weekly success'}],total_count:2}};
export const datasource={id:sourceID,name:'production-prometheus',display_name:'Production Prometheus',version:'1',provider:'prometheus',configuration_format:'prometheus-http-v1',endpoint:'https://metrics.example.test',tls:{insecure_skip_verify:false,ca_pem:null},authentication:{mode:'none',username:null,password_configured:false,token_configured:false},polling_interval_seconds:60,settling_delay_seconds:60,agent_id:agentID,created_at:start,updated_at:start};
export const history={items:observations,next_cursor:'',earliest_queryable_interval_start:'2026-11-01T00:00:00Z',inspected_start:start,inspected_end:end,missing_intervals:[{interval_start:'2026-11-11T09:43:00Z',interval_end:'2026-11-11T09:45:00Z'},{interval_start:'2026-11-11T09:52:00Z',interval_end:'2026-11-11T09:53:00Z'}]};
export function linkedStatus(index:number):CurrentStatus {
  const template=overview(services[0]!.id).items[2]!;
  const resource=sli.linked_slos!.items[index]!;
  return {...template,slo:{...template.slo,id:resource.id,name:resource.name,display_name:resource.display_name,sli_id:sliID,sli_display_name:sli.display_name,target:index===0?'0.999':'0.995',rolling_window_seconds:(index===0?30:7)*86400},generation:{generation_id:`60000000-0000-4000-8000-${String(index+1).padStart(12,'0')}`,source_generation:'1',measurement_revision:'1',definition_revision:'1',exclusion_revision:'0',calculation_version:'visual-fixture',evaluated_through:template.current.evaluation_at,published_at:template.current.evaluation_at,calculation_lag_seconds:0}};
}

export async function installSliDetailFixture(page:Page,resource:Sli=sli,pageHistory:SliObservationPage=history) {
  await page.clock.setFixedTime(new Date('2026-11-11T10:01:00Z'));
  await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'visual_fixture_only'}}));
  await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'visual-csrf',session:{user_id:agentID,login:'admin',role:'administrator',restricted:false}}}));
  await page.route('**/api/v1/datasources?*',route=>route.fulfill({json:{items:[datasource],next_cursor:''}}));
  await page.route(`**/api/v1/agents/${agentID}`,route=>route.fulfill({json:{id:agentID,name:'eu-collector',display_name:'EU collector',enabled:true,version:'1',created_at:history.inspected_start,updated_at:history.inspected_end},headers:{ETag:`"agent:${agentID}:1"`}}));
  await page.route('**/api/v1/slis?*',route=>route.fulfill({json:{items:[resource],next_cursor:''}}));
  await page.route(`**/api/v1/slis/${sliID}`,route=>route.fulfill({json:resource,headers:{ETag:`"sli:${sliID}:1"`}}));
  await page.route(`**/api/v1/slis/${sliID}/observations?*`,route=>route.fulfill({json:pageHistory}));
  await page.route('**/api/v1/slos/*/status',route=>route.fulfill({json:linkedStatus(route.request().url().includes('000000000002')?1:0)}));
}
