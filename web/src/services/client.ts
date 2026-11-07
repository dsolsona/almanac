import type {components} from '../api/generated.ts';
import type {AuthContext} from '../auth/client.ts';
import {request,write,SloFailure,sloClient} from '../slos/client.ts';
import {validLink,labelKeyPattern} from './model.ts';
export {SloFailure};
export type Service=components['schemas']['Service'];
export type ServiceReference=components['schemas']['ServiceReference'];
export type ServiceWrite=components['schemas']['ServiceCreateRequest'];
export type ServicePage=components['schemas']['ServicePage'];
export type Summary=components['schemas']['ServiceSloSummary'];
export type ServiceLink=components['schemas']['ServiceLink'];
export type OverviewItem=components['schemas']['SloOperationalOverviewItem'];
export const emptySummary:Summary={state:'no_slos',total:0,on_track:0,breached:0,unknown:0};
function catalogService(record:Service):Service{
 const value:Service={description:'',owner:'',contact:'',labels:{},links:[],dependencies:[],dependency_services:[],used_by:[],used_by_count:0,...record};
 const summary=value.slo_summary;
 const validReference=(record:ServiceReference)=>record&&typeof record.id==='string'&&/^[0-9a-f-]{36}$/.test(record.id)&&typeof record.name==='string'&&typeof record.display_name==='string'&&typeof record.owner==='string';
 if(typeof value.description!=='string'||typeof value.owner!=='string'||typeof value.contact!=='string'||!value.labels||typeof value.labels!=='object'||Array.isArray(value.labels)||!Array.isArray(value.links)||!Array.isArray(value.dependencies)||!Array.isArray(value.dependency_services)||!Array.isArray(value.used_by)||!value.dependencies.every(id=>typeof id==='string'&&/^[0-9a-f-]{36}$/.test(id))||!value.dependency_services.every(validReference)||!value.used_by.every(validReference)||!Number.isSafeInteger(value.used_by_count)||value.used_by_count!<value.used_by.length||!summary||![summary.total,summary.on_track,summary.breached,summary.unknown].every(n=>Number.isSafeInteger(n)&&n>=0)||summary.total!==summary.on_track+summary.breached+summary.unknown||summary.state!==(summary.breached?'breached':summary.unknown?'unknown':summary.total?'on_track':'no_slos')||!value.links.every(validLink)||Object.entries(value.labels).some(([key,values])=>!labelKeyPattern.test(key)||!Array.isArray(values)||values.some(item=>typeof item!=='string'))){throw new SloFailure('The server returned invalid service metadata.');}
 return value;
}
export const serviceClient={
 list:(query:{q?:string;owner?:string;coverage?:string;limit?:number;cursor?:string}={},signal?:AbortSignal)=>{
  const params=new URLSearchParams();for(const [key,value]of Object.entries(query))if(value!==undefined&&value!=='')params.set(key,String(value));
  return request<ServicePage>(`/api/v1/services?${params}`,{signal}).then(result=>({...result.value,items:result.value.items.map(catalogService)}));
 },
 get:(id:string,signal?:AbortSignal)=>request<Service>(`/api/v1/services/${id}`,{signal}).then(result=>({...result,value:catalogService(result.value)})),
 create:(body:ServiceWrite,context:AuthContext,key:string)=>request<Service>('/api/v1/services',write(context,body,key),true),
 update:(id:string,body:components['schemas']['ServiceMetadataWrite'],etag:string,context:AuthContext)=>request<Service>(`/api/v1/services/${id}`,write(context,body,undefined,etag),true),
 archive:(id:string,etag:string,context:AuthContext)=>request<void>(`/api/v1/services/${id}`,{method:'DELETE',headers:{'X-CSRF-Token':context.csrf_token,'If-Match':etag}},true),
 objectives:(id:string,signal?:AbortSignal,cursor='')=>sloClient.overview({service_id:id,limit:20,...(cursor?{cursor}:{})},signal),
};
