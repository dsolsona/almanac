import {expect,test} from '@playwright/test';
import {servicePage} from './support/service-fixtures.ts';
import {serviceFleet} from './support/service-fleet.ts';

test('graph gestures stay in the canvas and focus useful service neighborhoods @matrix',async({page})=>{
 const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
 const records=serviceFleet(),checkout=records.find(record=>record.name==='checkout')!,payments=records.find(record=>record.name==='payments')!;
 payments.slo_summary={state:'on_track',total:1,on_track:1,breached:0,unknown:0};checkout.slo_summary={state:'breached',total:1,on_track:0,breached:1,unknown:0};records.find(record=>record.name==='search')!.slo_summary={state:'unknown',total:1,on_track:0,breached:0,unknown:1};
 await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'fixture_only'}}));
 await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'csrf',session:{user_id:records[0]!.id,login:'Editor',role:'editor',restricted:false}}}));
 await page.route('**/api/v1/services**',route=>route.fulfill({json:{...servicePage(records),items:records,total:records.length,next_cursor:''}}));
 const find=async(name:string)=>{await page.getByRole('combobox',{name:'Find a service'}).fill(name);await page.getByRole('option').filter({has:page.getByText(name,{exact:true})}).click();};
 await page.goto('/services/dependencies');
 await expect(page.locator('.map-node')).toHaveCount(records.length);
 await expect(page.getByRole('heading',{name:'Relationships',exact:true})).toHaveCount(0);
 if(page.viewportSize()!.width<=860){
  await expect(page.locator('.graph-mobile')).toBeVisible();
  await expect(page.getByRole('button',{name:'Zoom in',includeHidden:true})).toBeHidden();
  await find(records.at(-1)!.display_name);
  await expect(page.locator('.graph-mobile .selected-focus')).toContainText(records.at(-1)!.display_name);expect(errors).toEqual([]);return;
 }
 const canvas=page.locator('.service-network'),zoom=page.getByLabel('Graph zoom',{exact:true});
 const finderBox=(await page.getByRole('combobox',{name:'Find a service'}).boundingBox())!;
 for(const control of [page.getByRole('button',{name:'Reset view',exact:true}),page.getByRole('button',{name:'Zoom out',exact:true}),page.getByRole('button',{name:'Graph help',exact:true})]){
  const box=(await control.boundingBox())!;expect(Math.abs(box.y+box.height/2-finderBox.y-finderBox.height/2)).toBeLessThan(1);
 }
 const help=page.getByRole('button',{name:'Graph help',exact:true});
 await expect(page.getByText('Drag to pan · Scroll to zoom · Arrow keys pan, +/− zoom, 0 fits all',{exact:true})).toBeHidden();
 await help.click();await expect(page.getByText('Drag to pan · Scroll to zoom · Arrow keys pan, +/− zoom, 0 fits all',{exact:true})).toBeVisible();
 await help.press('Escape');await expect(page.getByText('Drag to pan · Scroll to zoom · Arrow keys pan, +/− zoom, 0 fits all',{exact:true})).toBeHidden();
 const core=(name:string)=>page.locator(`.map-node[data-id="${records.find(record=>record.display_name===name)!.id}"] .map-node-core`);
 const selectedCore=page.locator('.map-node.selected .map-node-core');
 const center=async()=>{const box=(await selectedCore.boundingBox())!,viewport=(await canvas.boundingBox())!;expect(Math.abs(box.x+box.width/2-viewport.x-viewport.width/2)).toBeLessThan(1);expect(Math.abs(box.y+box.height/2-viewport.y-viewport.height/2)).toBeLessThan(1);};
 const initialZoom=await zoom.textContent();
 const boxes=await page.locator('.map-node').evaluateAll(nodes=>nodes.map(node=>node.getBoundingClientRect().toJSON()));
 for(const [index,a]of boxes.entries())for(const b of boxes.slice(index+1))expect(a.right<=b.left||b.right<=a.left||a.bottom<=b.top||b.bottom<=a.top).toBe(true);
 const textHeights=await page.locator('.map-node text').evaluateAll(nodes=>nodes.map(node=>node.getBoundingClientRect().height));
 expect(Math.min(...textHeights)).toBeGreaterThan(page.viewportSize()!.width>=1400?10:7);
 const positions=await page.locator('.map-node').evaluateAll(nodes=>nodes.map(node=>node.getAttribute('transform')));
 await page.screenshot({path:`../.local/services-parity/fleet-overview-${test.info().project.name}.png`,fullPage:true});
 const edge=(await canvas.boundingBox())!,scene=page.locator('.service-camera-scene'),beforeLeave=await scene.getAttribute('transform');
 await page.mouse.move(edge.x+1,edge.y+1);await page.mouse.down();await page.mouse.move(edge.x-2,edge.y+1);await page.mouse.up();await page.mouse.move(edge.x+edge.width/2,edge.y+edge.height/2);
 await expect(scene).toHaveAttribute('transform',beforeLeave!);
 expect(errors).toEqual([]);
 await find(checkout.display_name);await center();
 expect(Number((await zoom.textContent())!.replace('%',''))).toBeGreaterThan(Number(initialZoom!.replace('%','')));
 await find(payments.display_name);await center();
 const viewport=(await canvas.boundingBox())!,anchor={x:viewport.x+viewport.width*.65,y:viewport.y+viewport.height*.5},before=(await selectedCore.boundingBox())!,scrollBefore=await page.evaluate(()=>scrollY);
 await page.mouse.move(anchor.x,anchor.y);await page.mouse.wheel(0,-120);
 await expect.poll(async()=>(await selectedCore.boundingBox())!.width).toBeGreaterThan(before.width);
 const after=(await selectedCore.boundingBox())!,ratio=after.width/before.width;
 expect(Math.abs(after.x+after.width/2-(anchor.x+(before.x+before.width/2-anchor.x)*ratio))).toBeLessThan(1);
 expect(Math.abs(after.y+after.height/2-(anchor.y+(before.y+before.height/2-anchor.y)*ratio))).toBeLessThan(1);
 expect(await page.evaluate(()=>scrollY)).toBe(scrollBefore);
 await find(checkout.display_name);await center();
 const inventory=(await core('Payments').boundingBox())!;
 await page.mouse.move(inventory.x+inventory.width/2,inventory.y+inventory.height/2);await page.mouse.down();await page.mouse.move(inventory.x+inventory.width/2-70,inventory.y+inventory.height/2+45,{steps:8});await page.mouse.up();
 const dragged=(await core('Payments').boundingBox())!;expect(dragged.x-inventory.x).toBeCloseTo(-70,0);expect(dragged.y-inventory.y).toBeCloseTo(45,0);
 await expect(page.locator('.graph-details')).toContainText('Checkout');
 const quiet=page.locator(`.map-node[data-id="${records[0]!.id}"]`);await quiet.focus();await expect(quiet).toHaveCSS('opacity','1');
 await find(checkout.display_name);
 await find(records.at(-1)!.display_name);await center();
 await expect(page.locator('.graph-details')).toContainText(records.at(-1)!.display_name);
 expect(await page.locator('.map-node').evaluateAll(nodes=>nodes.map(node=>node.getAttribute('transform')))).toEqual(positions);
 await page.getByRole('button',{name:'Reset view',exact:true}).click();await expect(zoom).toHaveText(initialZoom!);
 for(const node of await page.locator('.map-node-core').all()){const box=(await node.boundingBox())!,view=(await canvas.boundingBox())!;expect(box.x).toBeGreaterThanOrEqual(view.x);expect(box.y).toBeGreaterThanOrEqual(view.y);expect(box.x+box.width).toBeLessThanOrEqual(view.x+view.width);expect(box.y+box.height).toBeLessThanOrEqual(view.y+view.height);}
 await canvas.focus();await canvas.press('=');expect(await zoom.textContent()).not.toBe(initialZoom);
 await canvas.press('0');await expect(zoom).toHaveText(initialZoom!);
 const leftBefore=(await selectedCore.boundingBox())!.x;await canvas.press('ArrowRight');expect((await selectedCore.boundingBox())!.x-leftBefore).toBeCloseTo(-48,0);
 for(let index=0;index<35&&await page.getByRole('button',{name:'Zoom in',exact:true}).isEnabled();index++)await page.getByRole('button',{name:'Zoom in',exact:true}).click();
 await expect(zoom).toHaveText('400%');await expect(page.getByRole('button',{name:'Zoom in',exact:true})).toBeDisabled();
 for(let index=0;index<40&&await page.getByRole('button',{name:'Zoom out',exact:true}).isEnabled();index++)await page.getByRole('button',{name:'Zoom out',exact:true}).click();
 await expect(page.getByRole('button',{name:'Zoom out',exact:true})).toBeDisabled();
 await page.getByRole('button',{name:'Reset view',exact:true}).click();await expect(zoom).toHaveText(initialZoom!);
 await page.getByRole('button',{name:'Owner groups',exact:true}).click();
 await page.screenshot({path:`../.local/services-parity/fleet-owners-${test.info().project.name}.png`,fullPage:true});
 const filters=page.getByRole('group',{name:'Service SLO status'});
 await filters.getByRole('button',{name:'Breached',exact:true}).click();await expect(page.locator('.map-node[data-filter-match="true"]')).toHaveCount(1);
 await expect(page.locator('.map-node')).toHaveCount(12);
 for(const name of ['API gateway','Cart','Payments','Orders']){
  const node=page.locator(`.map-node[data-id="${records.find(record=>record.display_name===name)!.id}"]`);
  await expect(node).toHaveAttribute('data-filter-match','false');expect(Number(await node.evaluate(node=>getComputedStyle(node).opacity))).toBeLessThan(.6);
 }
 await expect(page.locator(`.map-node[data-id="${records.find(record=>record.name==='configuration')!.id}"]`)).toHaveCount(0);
 const paymentContext=page.getByRole('button',{name:'Select Payments, On track, 1 SLO',exact:true});await paymentContext.focus();await expect(paymentContext).toHaveCSS('opacity','1');
 await paymentContext.press('Enter');await expect(page.locator('.graph-details')).toContainText('Payments');
 await filters.getByRole('button',{name:'Unknown',exact:true}).click();await expect(page.locator('.map-node[data-filter-match="true"]')).toHaveCount(2);
 await filters.getByRole('button',{name:'On track',exact:true}).click();await expect(page.locator('.map-node[data-filter-match="true"]')).toHaveCount(3);
 for(const node of await page.locator('.map-node[data-filter-match="true"]').all())expect(['on_track','breached','unknown']).toContain(await node.getAttribute('data-slo-state'));
 await page.getByRole('button',{name:'Reset view',exact:true}).click();await expect(page.locator('.map-node')).toHaveCount(50);
 await expect(filters.getByRole('button',{name:'On track',exact:true})).toHaveAttribute('aria-pressed','false');
 expect(await canvas.evaluate(node=>node.parentElement!.scrollHeight<=node.parentElement!.clientHeight)).toBe(true);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.evaluate(()=>scrollTo(0,0));await page.mouse.move(page.viewportSize()!.width-4,40);const outsideBefore=await page.evaluate(()=>scrollY);await page.mouse.wheel(0,250);await expect.poll(()=>page.evaluate(()=>scrollY)).toBeGreaterThan(outsideBefore);
 await page.screenshot({path:`../.local/services-parity/camera-${test.info().project.name}.png`,fullPage:true});
 expect(errors).toEqual([]);
});
