import {expect,test} from '@playwright/test';
test('point tooltip stays compact without shifting the graph or covering selection',{tag:'@matrix'},async({page},info)=>{
 await page.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),info.project.name.endsWith('dark')?'dark':'light');await page.goto('/design-system');await page.getByRole('button',{name:'Charts',exact:true}).click();
 const chart=page.locator('.slo-history-chart.budget'),svg=chart.locator('svg');const before=await chart.boundingBox();
 await chart.focus();
 for(const key of ['Home','End']){
 await page.keyboard.press(key);const tip=chart.getByRole('tooltip');await expect(tip).toBeVisible();const box=(await tip.boundingBox())!,point=(await chart.locator('.inspection-dot').boundingBox())!;
 expect(box.width).toBeLessThanOrEqual(260);expect(box.x).toBeGreaterThanOrEqual(before!.x);expect(box.x+box.width).toBeLessThanOrEqual(before!.x+before!.width);
 expect(box.x+box.width<point.x||box.x>point.x+point.width||box.y+box.height<point.y||box.y>point.y+point.height).toBe(true);
 expect((await chart.boundingBox())!.height).toBe(before!.height);
 await chart.screenshot({path:`../.local/catalogue-tooltip-${info.project.name}-${key}.png`});
 }
 await page.keyboard.press('Escape');await expect(chart.getByRole('tooltip')).toHaveCount(0);
 const bounds=(await svg.boundingBox())!;await page.mouse.move(bounds.x+bounds.width*.45,bounds.y+bounds.height*.5);await expect(chart.getByRole('tooltip')).toBeVisible();expect((await chart.boundingBox())!.height).toBe(before!.height);
 await page.screenshot({path:`../.local/catalogue-hover-${info.project.name}.png`,fullPage:true});
 await page.mouse.move(0,0);await expect(chart.getByRole('tooltip')).toHaveCount(0);
});


test('narrow-chart middle point stays clear when the pointer is above it',{tag:'@matrix'},async({page},info)=>{
 await page.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),info.project.name.endsWith('dark')?'dark':'light');await page.goto('/design-system');await page.getByRole('button',{name:'Charts',exact:true}).click();
 const chart=page.locator('.slo-history-chart.budget');await chart.scrollIntoViewIfNeeded();const marker=(await chart.locator('.qualified-point.partial').boundingBox())!;
 await page.mouse.move(marker.x+marker.width/2,marker.y-55);await expect(chart.getByRole('tooltip')).toContainText('65%');const tip=(await chart.getByRole('tooltip').boundingBox())!,point=(await chart.locator('.inspection-dot').boundingBox())!;
 expect(tip.x+tip.width<point.x||tip.x>point.x+point.width||tip.y+tip.height<point.y||tip.y>point.y+point.height).toBe(true);
 await page.screenshot({path:`../.local/catalogue-middle-${info.project.name}.png`,fullPage:true});
});

test('tooltip follows vertical movement within the same sample',async({page})=>{
 await page.goto('/design-system');await page.getByRole('button',{name:'Charts',exact:true}).click();const chart=page.locator('.slo-history-chart.budget');await chart.scrollIntoViewIfNeeded();const marker=(await chart.locator('.qualified-point.partial').boundingBox())!;
 const x=marker.x+marker.width/2;await page.mouse.move(x,marker.y+10);await expect(chart.getByRole('tooltip')).toContainText('65%');const before=(await chart.getByRole('tooltip').boundingBox())!.y;
 await page.mouse.move(x,marker.y+40);await expect(chart.getByRole('tooltip')).toContainText('65%');await expect.poll(async()=>(await chart.getByRole('tooltip').boundingBox())!.y).not.toBe(before);
});
