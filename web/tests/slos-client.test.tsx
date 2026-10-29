import { afterEach, expect, it, vi } from 'vitest';
import { sloClient, SloFailure } from '../src/slos/client.ts';
import type { AuthContext } from '../src/auth/client.ts';
const context:AuthContext={csrf_token:'csrf',session:{user_id:'u',login:'editor',role:'editor',restricted:false}};
const id='11111111-1111-1111-1111-111111111111';
const service={id,name:'checkout',display_name:'Checkout',version:'1',created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z'};
afterEach(()=>vi.unstubAllGlobals());
it('rejects malformed list and evaluation responses rather than treating them as empty',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({items:'not a page'}),{status:200}))); await expect(sloClient.list()).rejects.toThrow('invalid response');
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({window:{},quality:{},performance:{},timeline:{items:[]}}),{status:200}))); await expect(sloClient.evaluation(id)).rejects.toThrow('invalid response');
});
it('does not accept a missing or mismatched resource ETag after create',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(service),{status:201,headers:{ETag:`"service:${id}:2"`}})));
 try { await sloClient.createService({name:'checkout',display_name:'Checkout'},context,'key'); throw new Error('expected failure'); } catch(e) {expect(e).toBeInstanceOf(SloFailure);expect((e as SloFailure).uncertain).toBe(true);}
});
it('rejects swapped page and resource envelopes and a mismatched resource ID',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(service),{status:200,headers:{ETag:`"service:${id}:1"`}}))); await expect(sloClient.services()).rejects.toThrow('invalid response');
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({items:[service],next_cursor:''}),{status:200}))); await expect(sloClient.service(id)).rejects.toThrow('invalid response');
 const other='22222222-2222-2222-2222-222222222222'; vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...service,id:other}),{status:200,headers:{ETag:`"service:${other}:1"`}}))); await expect(sloClient.service(id)).rejects.toThrow('invalid response');
});
it('rejects an evaluation for a different SLO',async()=>{
 const instant='2026-01-01T00:00:00Z',other='22222222-2222-2222-2222-222222222222';const data={snapshot_kind:'current_evaluation',requested_at:instant,effective_end:instant,objective:{slo_id:other},window:{effective_start:instant,end:instant},quality:{state:'no_data',freshness:'unknown',expected_intervals:0,observed_intervals:0,known_empty_intervals:0,missing_intervals:0,zero_traffic_intervals:0,expired_intervals:0,coverage:null},performance:{state:'unknown',observed_state:null,provisional:false,good:null,total:null,bad:null,attainment:null,allowed_budget:null,remaining_budget:null,consumed_fraction:null,remaining_fraction:null,burn:null,budget_unit:'events'},timeline:{start:instant,end:instant,cadence_seconds:60,max_intervals:1500,items:[]}};
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(data),{status:200})));await expect(sloClient.evaluation(id)).rejects.toThrow('invalid response');
});
it('shows safe guidance for an invalid historical start without echoing server text',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({code:'invalid_request',field_errors:[{field:'evaluation_valid_from',code:'historical_start_invalid',message:'untrusted server text'}]}),{status:400})));
 await expect(sloClient.create({name:'availability',display_name:'Availability',service_id:id,sli_id:id,target:'0.99',rolling_window_seconds:60,evaluation_valid_from:'2026-01-01T00:00:00Z'},context,'key')).rejects.toThrow('Refresh the eligible range');
});
