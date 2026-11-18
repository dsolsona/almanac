import {expect,test} from '@playwright/test';
import type {components} from '../src/api/generated.ts';
import {api,expectOk,unique} from './support/api.ts';
import {compose,startAgent} from './support/stack.ts';

test('save, edit, disable and enable an alert policy through the deployed UI',async({page},info)=>{
 const name=unique('alert-journey');
 const registered=await expectOk(api(page,'POST','/api/v1/agents',{name:`collector-${name}`,display_name:'Alert collector'}));
 startAgent(registered.body.token);
 const agentID=registered.body.agent.id;
 await expect.poll(async()=>(await api(page,'GET',`/api/v1/agents/${agentID}`)).body.runtime?.reachability,{timeout:30_000}).toBe('connected');
 const source=await expectOk(api(page,'POST','/api/v1/datasources',{name,display_name:name,provider:'prometheus',configuration_format:'prometheus-http-v1',endpoint:'http://victoriametrics:8428',tls:{insecure_skip_verify:false,ca_pem:null},authentication:{mode:'none',username:null,password_secret:{action:'clear'},token_secret:{action:'clear'}},polling_interval_seconds:60,agent_id:agentID}));
 // This journey exercises alert policy management on a configured collector.
 // Let the new assignment finish before provisioning the SLI that uses it.
 await expect.poll(async () => {
   const providers = await api<components['schemas']['ProviderPage']>(page, 'GET', `/api/v1/providers?agent_id=${agentID}`);
   return providers.body.items?.find(provider => provider.provider === 'prometheus')?.availability;
 }, { timeout: 30_000 }).toBe('available');
 const creation = await api(page,'POST','/api/v1/slis',{name,display_name:name,datasource_id:source.body.id,measurement_kind:'event_counts',component_mode:'interval_count',good_query:'vector(99)',total_query:'vector(100)'});
 if (creation.status >= 500) {
   let evidence = 'PostgreSQL lock diagnostics unavailable.';
   try {
     // Keep timing and relation names without publishing statements or parameter values.
     const logs = compose('logs', '--no-color', '--tail', '120', 'postgres');
     const failures = logs.split(/\n(?=[^\n]*postgres-1\s+\|.*\b(?:ERROR|LOG|FATAL|WARNING):)/)
       .filter(entry => /ERROR:\s+(?:canceling statement due to lock timeout|deadlock detected|could not serialize access)/.test(entry))
       .map(entry => ({
         failure: entry.match(/ERROR:\s+(canceling statement due to lock timeout|deadlock detected|could not serialize access)/)?.[1],
         time: entry.match(/\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(?:\.\d+)? UTC/)?.[0],
         relations: [...new Set(entry.match(/public\.almanac_[a-z_0-9]+/g) ?? [])],
         advisoryLock: entry.includes('pg_advisory_xact_lock'),
       }));
     evidence = JSON.stringify(failures);
   } catch { /* Keep the original HTTP failure if diagnostics are unavailable. */ }
   await info.attach('postgres-lock-diagnostics', {body:evidence,contentType:'text/plain'});
 }
 const measurement=await expectOk(Promise.resolve(creation));
 const service=await expectOk(api(page,'POST','/api/v1/services',{name,display_name:'Alert journey service'}));
 const objective=await expectOk(api(page,'POST','/api/v1/slos',{name,display_name:`${name} availability`,service_id:service.body.id,sli_id:measurement.body.id,target:'0.999',rolling_window_seconds:2592000}));
 await page.goto('/alerts');
 await expect(page.getByRole('heading',{name:'Alerts',exact:true})).toBeVisible();
 await page.getByRole('link',{name:'New alert policy',exact:true}).click();
 await page.getByLabel('Alert policy name').fill(name);
 await page.getByRole('combobox',{name:'SLO',exact:true}).fill(name);
 await page.getByRole('option',{name:new RegExp(name)}).click();
 await page.getByRole('button',{name:'Create alert policy',exact:true}).click();
 await expect(page).toHaveURL(/\/alerts\/policies$/);
 await expect(page.getByRole('status').filter({hasText:'Alert policy saved.'})).toHaveText('Alert policy saved.');
 const savedLink=page.getByRole('link',{name,exact:true});
 const policyID=(await savedLink.getAttribute('href'))!.split('/').at(-1)!;
 await page.screenshot({path:'../.local/alert-ui-evidence/after/deployed-policy-created.png',fullPage:true});
 await info.attach('policy-created-list',{body:await page.screenshot({fullPage:true}),contentType:'image/png'});
 await savedLink.click();
 await expect(page.getByLabel('Alert policy name')).toHaveValue(name);
 let saved=await expectOk(api(page,'GET',`/api/v1/alert-policies/${policyID}`));
 expect(saved.body.slo_id).toBe(objective.body.id);expect(saved.body.severity).toBe('medium');
 expect(saved.body.conditions.map((condition:any)=>[condition.short_window_seconds,condition.long_window_seconds,condition.threshold])).toEqual([[300,3600,'14.4'],[1800,21600,'6']]);
 const revision=saved.body.evaluation_revision;
 await page.getByLabel('Alert policy name').fill(name+' renamed');
 await page.getByRole('button',{name:'Save changes',exact:true}).click();
 await expect(page).toHaveURL(/\/alerts\/policies$/);
 await expect(page.getByRole('link',{name:name+' renamed',exact:true})).toBeVisible();
 await page.getByRole('link',{name:name+' renamed',exact:true}).click();
 await expect(page.getByRole('heading',{name:name+' renamed',exact:true})).toBeVisible();
 saved=await expectOk(api(page,'GET',`/api/v1/alert-policies/${policyID}`));expect(saved.body.evaluation_revision).toBe(revision);
 await page.getByRole('button',{name:'Disable policy',exact:true}).click();
 await expect(page.getByRole('button',{name:'Enable policy',exact:true})).toBeVisible();
 await page.reload();await expect(page.getByRole('button',{name:'Enable policy',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Enable policy',exact:true}).click();
 await expect(page.getByRole('button',{name:'Disable policy',exact:true})).toBeVisible();
 saved=await expectOk(api(page,'GET',`/api/v1/alert-policies/${policyID}`));expect(BigInt(saved.body.evaluation_revision)).toBeGreaterThan(BigInt(revision));
 await page.goto('/alerts/policies');
 await page.getByRole('searchbox',{name:'Search policies'}).fill(name);
 await expect(page.getByRole('link',{name:name+' renamed',exact:true})).toBeVisible();
 const records=await expectOk(api(page,'GET',`/api/v1/alerts?scope=all&slo_id=${objective.body.id}`));expect(records.body.total).toBe('0');
 await info.attach('alert-management-acceptance',{body:JSON.stringify({policyID,sloID:objective.body.id,revision:saved.body.evaluation_revision,productionWorkerActivated:false}),contentType:'application/json'});
});
