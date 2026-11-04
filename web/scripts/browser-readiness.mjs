import assert from 'node:assert/strict';
import { expect } from '@playwright/test';

// Linux Chromium coalesces IP-address changes for two seconds. Docker startup
// must settle before measuring navigation or submitting a non-repeatable login.
export async function browserTransportReady(browser, url) {
  const endpoint=new URL('/readyz',url);
  assert.equal(endpoint.protocol,'https:');assert.equal(endpoint.hostname,'localhost');
  const context=await browser.newContext(),page=await context.newPage();
  let healthySince=null;
  try {
    await expect.poll(async()=>{
      try {
        const response=await page.goto(endpoint.href,{timeout:1000,waitUntil:'domcontentloaded'});
        if(response.status()!==200){healthySince=null;return false;}
        healthySince??=performance.now();
        return performance.now()-healthySince>=2000;
      }catch{healthySince=null;return false;}
    },{timeout:5000,intervals:[250]}).toBe(true);
    const healthy_ms=performance.now()-healthySince;
    assert.ok(healthySince!==null&&healthy_ms>=2000,'browser transport did not remain healthy through network settlement');
    return {healthy_ms};
  } finally {await context.close();}
}
