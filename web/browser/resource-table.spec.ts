import {expect,test} from '@playwright/test';

test('long assigned agent names remain readable in resource tables @matrix',async({page})=>{
 const id='00000000-0000-4000-8000-000000000001',agent='00000000-0000-4000-8000-000000000002',at='2026-11-11T12:00:00Z',name='Agent'.repeat(25);
 await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'fixture_only'}}));
 await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'fixture-csrf',session:{user_id:id,login:'Admin',role:'administrator',restricted:false}}}));
 await page.route('**/api/v1/agents?*',route=>route.fulfill({json:{items:[{id:agent,name:'collector',display_name:name,enabled:true}],next_cursor:''}}));
 await page.route('**/api/v1/slis?*',route=>route.fulfill({json:{items:[],next_cursor:''}}));
 await page.route('**/api/v1/datasources?*',route=>route.fulfill({json:{items:[{id,name:'metrics',display_name:'Production metrics',version:'1',provider:'prometheus',configuration_format:'prometheus-http-v1',endpoint:'https://metrics.example.test',tls:{insecure_skip_verify:false,ca_pem:null},authentication:{mode:'none',username:null,password_configured:false,token_configured:false},polling_interval_seconds:60,settling_delay_seconds:60,agent_id:agent,created_at:at,updated_at:at}],next_cursor:''}}));
 await page.goto('/datasources');
 const value=page.getByText(name,{exact:true});
 await expect(value).toBeVisible();
 const panel=page.getByRole('region',{name:'Datasource table'});
 await panel.focus(); await expect(panel).toBeFocused();
 const bounds=await value.boundingBox(),frame=await panel.boundingBox();
 expect(bounds!.x+bounds!.width).toBeLessThanOrEqual(frame!.x+frame!.width);
 expect(await value.evaluate(node=>node.scrollWidth<=node.clientWidth)).toBe(true);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
