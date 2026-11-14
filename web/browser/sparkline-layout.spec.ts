import { expect, test } from '@playwright/test';

test('budget sparkline retains geometry, gaps and theme across real size changes', { tag: '@matrix' }, async ({ page }, testInfo) => {
  const errors:string[]=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto('/design-system');
  await page.getByRole('button',{name:'Charts',exact:true}).click();
  const panel=page.getByRole('region',{name:'Rolling budget sparkline',exact:true});
  const chart=panel.locator('.slo-reference-spark[data-chart-ready]');
  const svg=chart.locator('svg');
  await expect(chart).toHaveAttribute('data-chart-ready','true');
  await expect(svg).toBeVisible();
  const geometry=()=>chart.evaluate(element=>({
    width:element.clientWidth,height:element.clientHeight,
    svgWidth:Number(element.querySelector('svg')?.getAttribute('width')),
    svgHeight:Number(element.querySelector('svg')?.getAttribute('height')),
  }));
  const aligned=async()=>{const value=await geometry();expect(value.svgWidth).toBe(value.width);expect(value.svgHeight).toBe(value.height);};
  await aligned();
  const paths=()=>svg.locator('path[stroke]:not([stroke="none"])');
  const original=await paths().first().getAttribute('d');
  expect(original).toMatch(/M.*L.*M/); // the missing sample breaks the budget line
  await panel.screenshot({path:testInfo.outputPath('sparkline.png')});
  for(const [width,height] of [[120,24],[240,36],[140,24]]) {
    await chart.evaluate((element,size)=>{element.style.width=`${size[0]}px`;element.style.height=`${size[1]}px`;},[width!,height!]);
    await expect.poll(async()=>{const value=await geometry();return value.svgWidth===width&&value.svgHeight===height;}).toBe(true);
    await aligned();
    expect(await paths().first().getAttribute('d')).toMatch(/M.*L.*M/);
  }
  const stroke=await paths().first().getAttribute('stroke');
  await page.evaluate(()=>document.documentElement.classList.toggle('dark'));
  await expect.poll(()=>paths().first().getAttribute('stroke')).not.toBe(stroke);
  await expect(chart).toHaveAttribute('data-chart-ready','true');
  await aligned();
  expect(errors).toEqual([]);
});

test('budget sparkline renders after a hidden mount and hide/show cycle', { tag: '@matrix' }, async ({ page }) => {
  const errors:string[]=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto('/design-system');
  const hiddenStyle=await page.addStyleTag({content:'.slo-reference-spark { display:none !important; }'});
  await page.getByRole('button',{name:'Charts',exact:true}).click();
  const chart=page.getByRole('region',{name:'Rolling budget sparkline',exact:true}).locator('.slo-reference-spark[data-chart-ready]');
  await expect(chart.locator('svg')).toHaveCount(1);
  await expect(chart).toBeHidden();
  await hiddenStyle.evaluate(element=>element.remove());
  const ready=async()=>{
    await expect(chart).toBeVisible();
    await expect(chart).toHaveAttribute('data-chart-ready','true');
    await expect.poll(()=>chart.evaluate(element=>Number(element.querySelector('svg')?.getAttribute('width'))===element.clientWidth)).toBe(true);
    expect(await chart.locator('path[stroke]:not([stroke="none"])').first().getAttribute('d')).toMatch(/M.*L.*M/);
  };
  await ready();
  await chart.evaluate(element=>{element.style.display='none';});
  await expect(chart).toBeHidden();
  // Two frames let the native observer deliver the hidden-size change.
  await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
  await chart.evaluate(element=>{element.style.display='';});
  await ready();
  expect(errors).toEqual([]);
});
