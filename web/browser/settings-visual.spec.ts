import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { expect, test } from '@playwright/test';

// Fixed reads isolate layout differences; e2e/settings.spec.ts exercises real persistence.
const samples = [
 ['Alex Morgan','alex@example.com','administrator','active','2026-11-06T13:55:00Z'],
 ['Sam Rivera','sam@example.com','editor','active','2026-11-06T12:00:00Z'],
 ['Jo Chen','jo@example.com','viewer','active','2026-11-05T17:30:00Z'],
 ['Priya Shah','priya@example.com','editor','pending',null],
 ['Lee Park','lee@example.com','viewer','disabled','2026-10-20T10:18:00Z'],
 ['Robin Ellis','robin@example.com','editor','active','2026-11-06T13:30:00Z'],
 ['Noor Ali','noor@example.com','viewer','active','2026-11-03T09:45:00Z'],
 ['Jules Martin','jules@example.com','editor','active','2026-11-06T13:00:00Z'],
 ['Mika Ito','mika@example.com','viewer','pending',null],
 ['Drew Taylor','drew@example.com','viewer','active','2026-10-30T11:20:00Z'],
 ['Remy Laurent','remy@example.com','editor','active','2026-11-06T10:00:00Z'],
 ['Kim Santos','kim@example.com','viewer','disabled',null],
];

test('settings matches the approved reference in both themes at desktop and mobile sizes',async({browser},testInfo)=>{
 const users=samples.map(([full_name,email,role,status,last_sign_in],index)=>({id:`00000000-0000-4000-8000-${String(index+1).padStart(12,'0')}`,full_name,email,role,status,last_sign_in,version:'1',sign_in_history_known:index!==11,is_last_admin:index===0}));
 const root=resolve('..');
 const server=createServer(async(request,response)=>{
  const path=new URL(request.url??'/', 'http://localhost').pathname;
  if(!path.startsWith('/docs/design/mocks/')&&!path.startsWith('/web/public/fonts/')){response.writeHead(404).end();return;}
  try{const asset=resolve(root,'.'+path+(path.endsWith('/')?'index.html':''));if(!asset.startsWith(root+'/'))throw Error();const body=await readFile(asset);response.setHeader('Content-Type',asset.endsWith('.css')?'text/css':asset.endsWith('.js')?'text/javascript':asset.endsWith('.ttf')?'font/ttf':'text/html');response.end(body);}catch{response.writeHead(404).end();}
 });
 await new Promise<void>(done=>server.listen(0,'127.0.0.1',done));
 const address=server.address();if(!address||typeof address==='string')throw Error('reference unavailable');
 const output=resolve('../.local/settings-parity');await mkdir(output,{recursive:true});
 const geometry:unknown[]=[];
 try{
  for(const [width,height] of [[1440,1000],[360,900]])for(const theme of ['light','dark']){
   const context=await browser.newContext({viewport:{width,height},storageState:{cookies:[],origins:[]}});
   await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);
   const actual=await context.newPage();await actual.clock.setFixedTime(new Date('2026-11-06T14:00:00Z'));
   await actual.route('**/api/**',route=>route.fulfill({status:503,json:{code:'visual_fixture_only'}}));
   await actual.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'visual-fixture-csrf',session:{user_id:users[0]!.id,login:'alex@example.com',role:'administrator',restricted:false}}}));
   await actual.route('**/api/v1/account',route=>route.request().method()==='GET'?route.fulfill({json:users[0]}):route.continue());
   await actual.route('**/api/v1/account/activation/context',route=>route.fulfill({json:{full_name:'Priya Shah',email:'priya@example.com',purpose:'setup'}}));
   await actual.route('**/api/v1/users**',route=>{
    if(route.request().method()!=='GET')return route.continue();
    const url=new URL(route.request().url());const id=url.pathname.split('/')[4];
    return route.fulfill({json:id?users.find(user=>user.id===id):{items:users.slice(0,10),total:users.length,next_cursor:'visual-next'}});
   });
   const reference=await context.newPage();
   for(const [name,route,hash] of [['account','account','account'],['users','users','users'],['new','users/new','users/new'],['edit',`users/${users[1].id}`,'users/sam'],['password','password','password'],['setup','/activate#alu_setup_visual','activate/pending'],['disable',`users/${users[1].id}`,'users/sam']]){
    await actual.goto(route.startsWith('/')?route:'/settings/'+route);await actual.getByRole('heading',{level:1}).waitFor();
    if(name==='users')await actual.locator('tbody tr').first().waitFor();
    else await actual.getByLabel(name==='password'||name==='setup'?'Password':'Full name',{exact:true}).waitFor();
    await actual.evaluate(()=>document.fonts.ready);
    await reference.goto(`http://127.0.0.1:${address.port}/docs/design/mocks/settings/?theme=${theme}#${hash}`);
    await reference.evaluate(()=>document.fonts.ready);
    await reference.evaluate(()=>document.documentElement.style.removeProperty('--mono'));
    const declaredFonts=await Promise.all([actual,reference].map(target=>target.evaluate(()=>getComputedStyle(document.documentElement).getPropertyValue('--mono').trim())));
    expect(declaredFonts[0]).toBe(declaredFonts[1]);
    // Keep the declared design token equal, then resolve its generic fallback identically in CI.
    // Chromium otherwise chooses different installed mono faces after navigating between pages.
    for(const target of [actual,reference])await target.evaluate(()=>document.documentElement.style.setProperty('--mono','"Liberation Mono", monospace'));
    if(name==='disable'){for(const target of [actual,reference])await target.getByRole('button',{name:'Disable user',exact:true}).click();}
    for(const [kind,target] of [['actual',actual],['reference',reference]] as const){
     const file=`${kind}-${name}-${width}-${theme}.png`;
     await target.screenshot({path:resolve(output,file),fullPage:true});
     const selectors=kind==='actual'?'h1,.settings-tabs,.ui-form-section,.ui-field,.settings-form-actions,.settings-list-frame,.ui-toolbar,.settings-table,.settings-table thead,.settings-table tbody,.settings-table tr:first-child,.settings-table tr:last-child,.ui-pager,.ui-pager__size,.ui-pager__steps':'h1,.settings-nav,.section,.field,.form-actions,.list-frame,.p-toolbar,.tbl,.tbl thead,.tbl tbody,.tbl tr:first-child,.tbl tr:last-child,.pager,.pager select,.pager button';
     const styles=await target.locator(kind==='actual'?'.settings-page button,.settings-page input,.settings-page .ui-label': '#screen button,#screen input,#screen label').evaluateAll(nodes=>nodes.map(node=>{const style=getComputedStyle(node);return {class:node.className,text:node.textContent,font:style.font,synthesis:style.fontSynthesis,height:style.height,lineHeight:style.lineHeight,color:style.color,padding:style.padding};}));
     const cdp=await context.newCDPSession(target);await cdp.send('DOM.enable');await cdp.send('CSS.enable');const document=await cdp.send('DOM.getDocument');const node=await cdp.send('DOM.querySelector',{nodeId:document.root.nodeId,selector:kind==='actual'?'.settings-page .ui-button':'#screen .btn'});const fonts=node.nodeId?await cdp.send('CSS.getPlatformFontsForNode',{nodeId:node.nodeId}):null;await cdp.detach();
     geometry.push({kind,name,width,theme,styles,fonts,nodes:await target.locator(selectors).evaluateAll(nodes=>nodes.map(node=>({class:node.className,text:node.tagName==='H1'?node.textContent:'',rect:node.getBoundingClientRect().toJSON()})))});
    }
    await actual.evaluate(()=>scrollTo(0,0));await reference.evaluate(()=>scrollTo(0,0));
    const actualBox=await actual.locator(name==='disable'?'.ui-dialog[open]':'.settings-page').boundingBox(),referenceBox=await reference.locator(name==='disable'?'#confirmation':'#screen').boundingBox();
    const actualPixels=await actual.screenshot({fullPage:true,clip:actualBox!}),referencePixels=await reference.screenshot({fullPage:true,clip:referenceBox!});
    const snapshot=`settings-${name}-${width}-${theme}.png`,expectedPath=testInfo.snapshotPath(snapshot);
    await mkdir(dirname(expectedPath),{recursive:true});await writeFile(expectedPath,referencePixels);
    const difference=await actual.evaluate(async({actual,reference})=>{
      const decode=async(value:string)=>createImageBitmap(new Blob([Uint8Array.from(atob(value),char=>char.charCodeAt(0))],{type:'image/png'}));
      const a=await decode(actual),b=await decode(reference);
      const canvas=document.createElement('canvas');canvas.width=Math.max(a.width,b.width);canvas.height=Math.max(a.height,b.height);
      const context=canvas.getContext('2d',{willReadFrequently:true})!;
      context.drawImage(a,0,0);const first=context.getImageData(0,0,canvas.width,canvas.height).data;
      context.clearRect(0,0,canvas.width,canvas.height);context.drawImage(b,0,0);const second=context.getImageData(0,0,canvas.width,canvas.height).data;
      let different=0,minX=canvas.width,minY=canvas.height,maxX=0,maxY=0;
      const mask=context.createImageData(canvas.width,canvas.height);
      for(let i=0;i<first.length;i+=4){const changed=first[i]!==second[i]||first[i+1]!==second[i+1]||first[i+2]!==second[i+2]||first[i+3]!==second[i+3];mask.data.set(changed?[255,0,0,255]:[255,255,255,255],i);if(changed){different++;const x=(i/4)%canvas.width,y=Math.floor(i/4/canvas.width);minX=Math.min(minX,x);maxX=Math.max(maxX,x);minY=Math.min(minY,y);maxY=Math.max(maxY,y);}}
      context.putImageData(mask,0,0);
      return {actual:[a.width,a.height],reference:[b.width,b.height],different,total:canvas.width*canvas.height,bounds:different?[minX,minY,maxX,maxY]:null,diffImage:canvas.toDataURL('image/png').split(',')[1]};
    },{actual:actualPixels.toString('base64'),reference:referencePixels.toString('base64')});
    const {diffImage,...stats}=difference;
    await writeFile(resolve(output,`diff-${name}-${width}-${theme}.png`),Buffer.from(diffImage,'base64'));
    geometry.push({kind:'pixels',name,width,theme,...stats});
    expect.soft(actualPixels).toMatchSnapshot(snapshot,{maxDiffPixels:0});
    await expect(actual.locator('html')).toHaveClass(theme==='dark'?/dark/:/^$/);
    expect(await actual.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   }
   await context.close();
  }
 }finally{server.close();}
 await writeFile(resolve(output,'geometry.json'),JSON.stringify(geometry,null,2));
 await writeFile(resolve(output,'summary.json'),JSON.stringify(geometry.filter((entry:any)=>entry.kind==='pixels'),null,2));
 await writeFile(resolve(output,'users-layout.json'),JSON.stringify(geometry.filter((entry:any)=>entry.name==='users'&&entry.theme==='light'&&entry.kind!=='pixels').map((entry:any)=>({kind:entry.kind,width:entry.width,nodes:entry.nodes.map((node:any)=>({class:node.class,x:node.rect.x,y:node.rect.y,w:node.rect.width,h:node.rect.height}))})),null,2));
 await testInfo.attach('settings geometry',{path:resolve(output,'geometry.json'),contentType:'application/json'});
});
