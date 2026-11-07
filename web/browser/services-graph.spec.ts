import {expect,test} from '@playwright/test';
import {services,servicePage} from './support/service-fixtures.ts';

test('graph gestures stay in the canvas and focus useful service neighborhoods @matrix',async({page})=>{
 const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
 const records=[...services,...Array.from({length:20},(_,index)=>({...services[9]!,id:`00000000-0000-4000-8000-${String(index+100).padStart(12,'0')}`,name:`other-${index}`,display_name:`Other service ${index}`,owner:''}))];
 await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'fixture_only'}}));
 await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'csrf',session:{user_id:records[0]!.id,login:'Editor',role:'editor',restricted:false}}}));
 await page.route('**/api/v1/services**',route=>route.fulfill({json:{...servicePage(records),items:records,total:records.length,next_cursor:''}}));
 await page.goto('/services/dependencies');
 await expect(page.locator('.map-node')).toHaveCount(records.length);
 await expect(page.getByRole('heading',{name:'Relationships',exact:true})).toHaveCount(0);
 if(page.viewportSize()!.width<=860){
  await expect(page.locator('.graph-mobile')).toBeVisible();
  await expect(page.getByRole('button',{name:'Zoom in',includeHidden:true})).toBeHidden();
  await page.getByRole('combobox',{name:'Find a service'}).selectOption(records.at(-1)!.id!);
  await expect(page.locator('.graph-mobile .selected-focus')).toContainText('Other service 19');expect(errors).toEqual([]);return;
 }
 const canvas=page.locator('.service-network'),zoom=page.getByLabel('Graph zoom',{exact:true}),finder=page.getByRole('combobox',{name:'Find a service'});
 const core=(name:string)=>page.locator('.map-node').filter({hasText:name}).locator('.map-node-core');
 const selectedCore=page.locator('.map-node.selected .map-node-core');
 const center=async()=>{const box=(await selectedCore.boundingBox())!,viewport=(await canvas.boundingBox())!;expect(Math.abs(box.x+box.width/2-viewport.x-viewport.width/2)).toBeLessThan(1);expect(Math.abs(box.y+box.height/2-viewport.y-viewport.height/2)).toBeLessThan(1);};
 const initialZoom=await zoom.textContent();
 const edge=(await canvas.boundingBox())!,scene=page.locator('.service-camera-scene'),beforeLeave=await scene.getAttribute('transform');
 await page.mouse.move(edge.x+1,edge.y+1);await page.mouse.down();await page.mouse.move(edge.x-2,edge.y+1);await page.mouse.up();await page.mouse.move(edge.x+edge.width/2,edge.y+edge.height/2);
 await expect(scene).toHaveAttribute('transform',beforeLeave!);
 expect(errors).toEqual([]);
 await page.getByRole('button',{name:'Focus selected',exact:true}).click();await center();
 expect(Number((await zoom.textContent())!.replace('%',''))).toBeGreaterThan(Number(initialZoom!.replace('%','')));
 await finder.selectOption(services[1]!.id!);await center();
 const viewport=(await canvas.boundingBox())!,anchor={x:viewport.x+viewport.width*.65,y:viewport.y+viewport.height*.5},before=(await selectedCore.boundingBox())!,scrollBefore=await page.evaluate(()=>scrollY);
 await page.mouse.move(anchor.x,anchor.y);await page.mouse.wheel(0,-120);
 await expect.poll(async()=>(await selectedCore.boundingBox())!.width).toBeGreaterThan(before.width);
 const after=(await selectedCore.boundingBox())!,ratio=after.width/before.width;
 expect(Math.abs(after.x+after.width/2-(anchor.x+(before.x+before.width/2-anchor.x)*ratio))).toBeLessThan(1);
 expect(Math.abs(after.y+after.height/2-(anchor.y+(before.y+before.height/2-anchor.y)*ratio))).toBeLessThan(1);
 expect(await page.evaluate(()=>scrollY)).toBe(scrollBefore);
 await finder.selectOption(services[0]!.id!);await center();
 const inventory=(await core('Inventory').boundingBox())!;
 await page.mouse.move(inventory.x+inventory.width/2,inventory.y+inventory.height/2);await page.mouse.down();await page.mouse.move(inventory.x+inventory.width/2-70,inventory.y+inventory.height/2+45,{steps:8});await page.mouse.up();
 const dragged=(await core('Inventory').boundingBox())!;expect(dragged.x-inventory.x).toBeCloseTo(-70,0);expect(dragged.y-inventory.y).toBeCloseTo(45,0);
 await expect(page.locator('.graph-details')).toContainText('Checkout');
 await page.getByRole('button',{name:'Needs attention',exact:true}).click();
 for(const name of ['Checkout','Search']){const box=(await core(name).boundingBox())!,view=(await canvas.boundingBox())!;expect(box.x).toBeGreaterThanOrEqual(view.x);expect(box.y).toBeGreaterThanOrEqual(view.y);expect(box.x+box.width).toBeLessThanOrEqual(view.x+view.width);expect(box.y+box.height).toBeLessThanOrEqual(view.y+view.height);}
 await expect(page.locator('.graph-details')).toContainText('Checkout');
 await finder.selectOption(records.at(-1)!.id!);await center();
 await expect(page.locator('.graph-details')).toContainText('Other service 19');
 await page.getByRole('button',{name:'Fit all',exact:true}).click();await expect(zoom).toHaveText(initialZoom!);
 for(const node of await page.locator('.map-node-core').all()){const box=(await node.boundingBox())!,view=(await canvas.boundingBox())!;expect(box.x).toBeGreaterThanOrEqual(view.x);expect(box.y).toBeGreaterThanOrEqual(view.y);expect(box.x+box.width).toBeLessThanOrEqual(view.x+view.width);expect(box.y+box.height).toBeLessThanOrEqual(view.y+view.height);}
 await canvas.focus();await canvas.press('=');expect(await zoom.textContent()).not.toBe(initialZoom);
 await canvas.press('0');await expect(zoom).toHaveText(initialZoom!);
 const leftBefore=(await selectedCore.boundingBox())!.x;await canvas.press('ArrowRight');expect((await selectedCore.boundingBox())!.x-leftBefore).toBeCloseTo(-48,0);
 for(let index=0;index<35&&await page.getByRole('button',{name:'Zoom in',exact:true}).isEnabled();index++)await page.getByRole('button',{name:'Zoom in',exact:true}).click();
 await expect(zoom).toHaveText('400%');await expect(page.getByRole('button',{name:'Zoom in',exact:true})).toBeDisabled();
 for(let index=0;index<40&&await page.getByRole('button',{name:'Zoom out',exact:true}).isEnabled();index++)await page.getByRole('button',{name:'Zoom out',exact:true}).click();
 await expect(page.getByRole('button',{name:'Zoom out',exact:true})).toBeDisabled();
 await page.getByRole('button',{name:'Fit all',exact:true}).click();await expect(zoom).toHaveText(initialZoom!);
 await page.getByRole('button',{name:'Owner groups',exact:true}).click();await page.getByRole('button',{name:'List & map',exact:true}).click();
 await page.getByRole('button',{name:'Needs attention',exact:true}).click();
 expect(await canvas.evaluate(node=>node.parentElement!.scrollHeight<=node.parentElement!.clientHeight)).toBe(true);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.evaluate(()=>scrollTo(0,0));await page.mouse.move(page.viewportSize()!.width-4,40);const outsideBefore=await page.evaluate(()=>scrollY);await page.mouse.wheel(0,250);await expect.poll(()=>page.evaluate(()=>scrollY)).toBeGreaterThan(outsideBefore);
 await page.screenshot({path:`../.local/services-parity/camera-${test.info().project.name}.png`,fullPage:true});
 expect(errors).toEqual([]);
});
