import {expect,test,type Page} from '@playwright/test';
import {alertFixture,alertID} from './support/alert-fixtures.ts';
async function observe(page:Page){
 await page.addInitScript(()=>{Object.assign(window,{alertOutcomes:[]});addEventListener('almanac:journey',event=>{(window as unknown as {alertOutcomes:{journey:string;result:string}[]}).alertOutcomes.push((event as CustomEvent).detail);});});
}
const outcomes=(page:Page)=>page.evaluate(()=>(window as unknown as {alertOutcomes:{journey:string;result:string}[]}).alertOutcomes.filter(value=>value.result==='ready'||value.result==='error'));

test('failed alert detail reports error when its read settles',async({page})=>{
 await observe(page);await alertFixture(page,{tracing:true,occurrenceFailure:true});await page.goto(`/alerts/${alertID}`);
 await expect(page.getByRole('alert')).toBeVisible();
 await expect.poll(async()=>(await outcomes(page)).map(value=>[value.journey,value.result])).toEqual([['alert.detail','error']]);
});
test('initial policy readiness waits for its SLO choices',async({page})=>{
 let release!:()=>void;const delay=new Promise<void>(resolve=>{release=resolve;});
 await observe(page);const fixture=await alertFixture(page,{tracing:true,sloDelay:delay});
 try{
  await page.goto('/alerts/policies/new');await expect(page.getByRole('heading',{name:'New alert policy',exact:true})).toBeVisible();
  await expect.poll(fixture.sloRequests).toBeGreaterThan(0);
  expect(await outcomes(page)).toEqual([]);
  release();await expect.poll(async()=>(await outcomes(page)).map(value=>[value.journey,value.result])).toEqual([['alert.policy','ready']]);
 }finally{release();}
});
test('initial SLO choice failure is an error outcome',async({page})=>{
 await observe(page);await alertFixture(page,{tracing:true,sloFailure:true});await page.goto('/alerts/policies/new');
 await expect(page.getByRole('alert')).toBeVisible();
 await expect.poll(async()=>(await outcomes(page)).map(value=>[value.journey,value.result])).toEqual([['alert.policy','error']]);
});

test('retained burn evidence can be inspected by touch',async({browser,baseURL})=>{
 const context=await browser.newContext({baseURL,viewport:{width:360,height:800},hasTouch:true,isMobile:true});
 try{
  const page=await context.newPage();await alertFixture(page);await page.goto(`/alerts/${alertID}`);
  const plot=page.locator('.timeline-chart__plot');await expect(plot.locator('svg')).toBeVisible();
  const bounds=await plot.boundingBox();if(!bounds)throw new Error('Chart plot is unavailable.');
  await plot.tap({position:{x:bounds.width-20,y:bounds.height/2}});
  await expect(page.getByRole('tooltip')).toContainText('14.4');
 }finally{await context.close();}
});
