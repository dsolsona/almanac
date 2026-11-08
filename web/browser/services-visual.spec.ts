import {createServer} from 'node:http';
import {readFile,mkdir,writeFile} from 'node:fs/promises';
import {resolve,dirname} from 'node:path';
import {expect,test} from '@playwright/test';
import {services,graphServices,servicePage,overview} from './support/service-fixtures.ts';

for(const width of [1440,1024,390])for(const theme of ['light','dark'])
test(`services match the approved reference at ${width}px in ${theme}`,async({browser},testInfo)=>{
 const root=resolve('..'),output=resolve('../.local/services-parity');await mkdir(output,{recursive:true});
 const server=createServer(async(req,res)=>{const path=new URL(req.url??'/','http://localhost').pathname;if(!path.startsWith('/docs/design/mocks/')&&!path.startsWith('/web/public/fonts/')){res.writeHead(404).end();return;}try{const asset=resolve(root,'.'+path+(path.endsWith('/')?'index.html':''));if(!asset.startsWith(root+'/'))throw Error();const body=await readFile(asset);res.setHeader('Content-Type',asset.endsWith('.css')?'text/css':asset.endsWith('.js')?'text/javascript':asset.endsWith('.ttf')?'font/ttf':'text/html');res.end(body);}catch{res.writeHead(404).end();}});
 await new Promise<void>(done=>server.listen(0,'127.0.0.1',done));const address=server.address();if(!address||typeof address==='string')throw Error('reference server unavailable');
 const context=await browser.newContext({viewport:{width,height:1050},deviceScaleFactor:2,storageState:{cookies:[],origins:[]}});await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);
 const actual=await context.newPage(),reference=await context.newPage();await context.addInitScript(()=>document.addEventListener('DOMContentLoaded',()=>document.documentElement.style.overflowAnchor='none'));await actual.clock.setFixedTime(new Date('2026-11-06T13:42:00Z'));
 const errors:string[]=[];actual.on('pageerror',error=>errors.push(error.message));
 await actual.route('**/api/**',route=>route.fulfill({status:503,json:{code:'visual_fixture_only'}}));
 await actual.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'visual-csrf',session:{user_id:services[0]!.id,login:'Alex Morgan',role:'editor',restricted:false}}}));
 await actual.route('**/api/v1/services**',route=>{
  const url=new URL(route.request().url()),id=url.pathname.split('/')[4];
  if(id){const service=services.find(item=>item.id===id);return route.fulfill({json:service,headers:{ETag:`"service:${id}:1"`}});}
  if(url.searchParams.get('limit')==='200')return route.fulfill({json:{...servicePage(graphServices),items:graphServices,next_cursor:'',total:graphServices.length}});
  return route.fulfill({json:servicePage(services,url.searchParams.get('cursor')??'')});
 });
 await actual.route('**/api/v1/slos/overview**',route=>route.fulfill({json:overview(new URL(route.request().url()).searchParams.get('service_id')!)}));
 const summary:unknown[]=[];
 try{
  for(const [name,path,hash]of [['list','/services','list'],['detail',`/services/${services[0]!.id}`,'service/checkout'],['new','/services/new','new'],['edit',`/services/${services[0]!.id}/edit`,'edit/checkout'],['graph','/services/dependencies','map/a'],['graph-b','/services/dependencies','map/b']]as const){
   await actual.goto(path);await actual.getByRole('heading',{level:1}).waitFor();
   if(name==='list')await actual.locator('.services-table tbody tr').first().waitFor();
   if(name==='detail')await actual.locator('.service-slo-table tbody tr').first().waitFor();
   if(name==='edit')await expect(actual.getByLabel('Display name',{exact:true})).toHaveValue('Checkout');
   if(name.startsWith('graph')&&width>860)await actual.locator('.map-node').first().waitFor();
   if(name.startsWith('graph')&&width<=860)await actual.locator('.graph-mobile').waitFor();
   if(name==='graph-b')await actual.getByRole('button',{name:'Owner groups',exact:true}).click();
   await reference.goto(`http://127.0.0.1:${address.port}/docs/design/mocks/services/?theme=${theme}#${hash}`);await reference.locator('#screen h1').waitFor();
   for(const target of [actual,reference]){await target.evaluate(()=>document.fonts.ready);await target.evaluate(()=>document.documentElement.style.setProperty('--mono','"Liberation Mono", monospace'));}
   expect(await actual.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   const actualTarget=name.startsWith('graph')?width>860?'.map-desktop':'.graph-mobile':'.services-page',referenceTarget=name.startsWith('graph')?width>860?'.map-desktop':'.graph-mobile':'#screen';
   await actual.evaluate(()=>scrollTo(0,0));await reference.evaluate(()=>scrollTo(0,0));
   // Match origins and raster layers: the prototype review bar otherwise changes subpixel SVG painting.
   const origin=Math.max(100,(await actual.locator(actualTarget).boundingBox())!.y,(await reference.locator(referenceTarget).boundingBox())!.y);
   for(const [target,selector]of [[actual,actualTarget],[reference,referenceTarget]]as const)await target.locator(selector).evaluate((node,origin)=>{const box=node.getBoundingClientRect(),root=node.closest<HTMLElement>('.services-page,#screen')!;root.style.position='relative';root.style.top=`${origin-box.y}px`;if(node.matches('.map-desktop'))(node as HTMLElement).style.transform='translateZ(0)';},origin);
   const actualBox=await actual.locator(actualTarget).boundingBox(),referenceBox=await reference.locator(referenceTarget).boundingBox();
   // Expand fractional edges to complete CSS pixels; keep every content pixel in both crops.
   const fullPixels=(box:NonNullable<typeof actualBox>)=>({x:Math.floor(box.x),y:Math.floor(box.y),width:Math.ceil(box.x+box.width)-Math.floor(box.x),height:Math.ceil(box.y+box.height)-Math.floor(box.y)});
   for(const [target,box]of [[actual,actualBox!],[reference,referenceBox!]]as const)await target.evaluate(bottom=>document.body.style.minHeight=`${Math.ceil(bottom)+100}px`,box.y+box.height);
   const actualBytes=await actual.screenshot({fullPage:true,clip:fullPixels(actualBox!)}),referenceBytes=await reference.screenshot({fullPage:true,clip:fullPixels(referenceBox!)});
   await writeFile(resolve(output,`actual-${name}-${width}-${theme}.png`),actualBytes);await writeFile(resolve(output,`reference-${name}-${width}-${theme}.png`),referenceBytes);
   const snapshot=`services-${name}-${width}-${theme}.png`,expected=testInfo.snapshotPath(snapshot);await mkdir(dirname(expected),{recursive:true});await writeFile(expected,referenceBytes);
   const geometry=await Promise.all([actual,reference].map((target,index)=>target.locator(index===0?`${actualTarget} h1,${actualTarget} table,${actualTarget} .ui-form-section,${actualTarget} .ui-field,${actualTarget} .service-info,${actualTarget} .dependency-scene,${actualTarget} .ui-page-head,${actualTarget} .ui-toolbar,${actualTarget} .ui-pager,${actualTarget} .ui-form-actions`:`${referenceTarget} h1,${referenceTarget} table,${referenceTarget} .section,${referenceTarget} .field,${referenceTarget} .service-info,${referenceTarget} .dependency-scene,${referenceTarget} .p-head,${referenceTarget} .p-toolbar,${referenceTarget} .pager,${referenceTarget} .form-actions`).evaluateAll(nodes=>nodes.map(node=>({tag:node.tagName,class:node.className,rect:node.getBoundingClientRect().toJSON(),font:getComputedStyle(node).font})))));
   const controls=await Promise.all([actual,reference].map((target,index)=>target.locator((index===0?actualTarget:referenceTarget)+' :is(button,input,select,.ui-button,.ui-label,.ui-pager,.pager)').evaluateAll(nodes=>nodes.map(node=>({text:node.textContent,cls:node.className,rect:node.getBoundingClientRect().toJSON(),font:getComputedStyle(node).font})))));const paths=await Promise.all([actual,reference].map(target=>target.locator('.map-edge,.dependency-connections,.service-network,.ui-label__optional,.optional,.dependency-current-node,.dependency-current-node>*,.map-node-core,.service-owner-filter .ui-combobox__icon').evaluateAll(nodes=>nodes.map(node=>({cls:node.getAttribute('class'),matrix:node instanceof SVGGraphicsElement?[node.getScreenCTM()?.a,node.getScreenCTM()?.b,node.getScreenCTM()?.c,node.getScreenCTM()?.d,node.getScreenCTM()?.e,node.getScreenCTM()?.f]:null,style:node instanceof SVGGraphicsElement?['fill','stroke','stroke-width','opacity','shape-rendering','transform'].map(key=>[key,getComputedStyle(node).getPropertyValue(key)]):null,font:getComputedStyle(node).font,d:node.getAttribute('d'),viewBox:node.getAttribute('viewBox'),rect:node.getBoundingClientRect().toJSON()})))));summary.push({name,width,theme,actual:geometry[0],reference:geometry[1],controls,paths,actualBox,referenceBox});
   expect.soft(actualBytes).toMatchSnapshot(snapshot,{maxDiffPixels:0});
  }
  expect(errors).toEqual([]);
 }finally{await context.close();server.close();await writeFile(resolve(output,`geometry-${width}-${theme}.json`),JSON.stringify(summary,null,2));}
});
