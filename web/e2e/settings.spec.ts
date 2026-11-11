import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';

test('an administrator creates a user, shares setup, and controls real account access', async ({ page, browser },testInfo) => {
  const name = unique('Sam Rivera'), email = `${unique('sam')}@example.test`;
  await page.addInitScript(() => {
    const timing = { formUsableMs: 0, longTasks: [] as { start: number; duration: number }[] };
    Object.assign(window, { settingsLoadTiming: timing });
    new PerformanceObserver(list => {
      timing.longTasks.push(...list.getEntries().map(entry => ({ start: entry.startTime, duration: entry.duration })));
    }).observe({ type: 'longtask', buffered: true });
    const observer = new MutationObserver(() => {
      if (document.querySelector('#full-name') && document.querySelector('[role="radiogroup"]')) {
        timing.formUsableMs = performance.now();
        observer.disconnect();
      }
    });
    observer.observe(document, { childList: true, subtree: true });
  });
  const accountStarted=performance.now();
  await page.goto('/settings/account');
  await Promise.all([
    expect(page.getByRole('heading', { name:'Your account',exact:true })).toBeVisible(),
    expect(page.getByRole('radiogroup',{name:'Theme',exact:true})).toBeVisible(),
    expect(page.getByLabel('Full name',{exact:true})).toBeVisible(),
  ]);
  await page.evaluate(()=>document.fonts.ready);
  const accountUsableMs=performance.now()-accountStarted;
  const accountTrace = await page.evaluate(() => ({
    ...(window as unknown as { settingsLoadTiming: { formUsableMs: number; longTasks: { start: number; duration: number }[] } }).settingsLoadTiming,
    resources: performance.getEntriesByType('resource').map(entry => {
      const resource = entry as PerformanceResourceTiming;
      return { path: new URL(resource.name).pathname, start: resource.startTime, end: resource.responseEnd };
    }),
  }));
  await page.getByRole('radio',{name:'Dark',exact:true}).click();
  await expect(page.locator('html')).toHaveClass(/dark/);
  await page.getByRole('radio',{name:'Light',exact:true}).click();
  const usersStarted=performance.now();
  await page.getByRole('link',{name:'Users',exact:true}).click();
  await expect(page.locator('.settings-table tbody tr').first()).toBeVisible();await page.evaluate(()=>document.fonts.ready);
  const usersUsableMs=performance.now()-usersStarted;
  const timings = {fixture:'isolated real HTTP/PostgreSQL, initial user directory',accountUsableMs,usersUsableMs,accountTrace};
  console.info('Settings first-visit timings:',JSON.stringify(timings));
  await testInfo.attach('settings first-visit timings',{body:JSON.stringify(timings),contentType:'application/json'});
  expect(accountUsableMs).toBeLessThan(1000);expect(usersUsableMs).toBeLessThan(1000);
  await page.getByRole('button',{name:'Add user',exact:true}).click();
  await page.getByLabel('Full name',{exact:true}).fill(name);
  await page.getByLabel('Email',{exact:true}).fill(email);
  await page.getByRole('combobox',{name:'Role',exact:true}).fill('view');
  await expect(page.getByRole('option')).toHaveCount(1);
  await page.getByRole('option',{name:/Viewer/}).click();
  await expect(page.getByLabel('Password',{exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'Add user',exact:true}).click();
  const issuance = page.getByRole('dialog',{name:'Password setup link ready'});
  await expect(issuance).toBeVisible();
  const link = (await issuance.locator('code').textContent())!;
  await issuance.getByRole('button',{name:'Done',exact:true}).click();
  const userPath = new URL(page.url()).pathname;

  const person = await browser.newContext({ ignoreHTTPSErrors:true, storageState:{cookies:[],origins:[]} });
  try {
    const user = await person.newPage();
    await user.goto(link);
    await expect(user.getByRole('heading',{name:'Set your password',exact:true})).toBeVisible();
    await expect(user).not.toHaveURL(/#/);
    await expect(user.getByText(`Choose a password for ${email}.`,{exact:true})).toBeVisible();
    await user.getByLabel('Password',{exact:true}).fill('Sam has a very private password 61');
    await user.getByLabel('Confirm password',{exact:true}).fill('Sam has a very private password 61');
    await user.getByRole('button',{name:'Set password',exact:true}).click();
    await expect(user.getByRole('heading',{name:'Your account is ready',exact:true})).toBeVisible();
    await user.getByRole('link',{name:'Continue to sign in',exact:true}).click();
    await user.getByLabel('Login',{exact:true}).fill(email);
    await user.getByLabel('Password',{exact:true}).fill('Sam has a very private password 61');
    await user.getByRole('button',{name:'Sign in',exact:true}).click();
    await expect(user.getByRole('heading',{name:'Agents',exact:true})).toBeVisible();
    await user.goto('/settings/account');
    await expect(user.getByLabel('Full name',{exact:true})).toHaveValue(name);
    await expect(user.getByLabel('Sign-in email',{exact:true})).toHaveValue(email);
    await expect(user.getByRole('link',{name:'Users',exact:true})).toHaveCount(0);
    const forbidden = await api(user,'GET','/api/v1/users');
    expect(forbidden.status).toBe(403);
    await expect(user.getByRole('link', { name: 'Sessions', exact: true })).toHaveCount(0);
    expect((await api(user, 'GET', '/api/v1/settings/sessions')).status).toBe(403);
    expect((await api(user, 'PUT', '/api/v1/settings/sessions', { idle_minutes: 120, version: 1 })).status).toBe(403);
    await user.goto('/settings/sessions');
    await expect(user.getByText('Admin access required.', { exact: true })).toBeVisible();

    await page.reload();
    await expect(page.getByRole('button',{name:'Reset password',exact:true})).toBeEnabled();
    await page.getByRole('link',{name:'Users',exact:true}).first().click();
    await page.getByLabel('Search users',{exact:true}).fill(email);
    const row = page.getByRole('row').filter({has:page.getByRole('link',{name,exact:true})});
    await expect(row.locator('td').last()).not.toHaveText('Never signed in');
    await row.locator('td').nth(1).click();
    await expect(page).toHaveURL(new RegExp(userPath+'$'));

    await page.getByRole('button',{name:'Disable user',exact:true}).click();
    let dialog = page.getByRole('dialog',{name:`Disable ${name}?`});
    await expect(dialog.getByRole('button',{name:'Cancel',exact:true})).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(dialog.getByRole('button',{name:'Disable user',exact:true})).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(dialog.getByRole('button',{name:'Cancel',exact:true})).toBeFocused();
    await dialog.getByRole('button',{name:'Disable user',exact:true}).click();
    await expect(page.getByRole('button',{name:'Enable user',exact:true})).toBeVisible();
    expect((await api(user,'GET','/api/v1/account')).status).toBe(401);
    await page.getByRole('button',{name:'Enable user',exact:true}).click();
    dialog = page.getByRole('dialog',{name:`Enable ${name}?`});
    await dialog.getByRole('button',{name:'Enable user',exact:true}).click();
    await expect(page.getByRole('button',{name:'Reset password',exact:true})).toBeEnabled();
    await page.getByRole('button',{name:'Reset password',exact:true}).click();
    dialog = page.getByRole('dialog',{name:`Reset ${name}’s password?`});
    await dialog.getByRole('button',{name:'Generate link',exact:true}).click();
    await expect(page.getByRole('dialog',{name:'Password reset link ready'})).toBeVisible();
    await page.getByRole('dialog').getByRole('button',{name:'Done',exact:true}).click();
    await page.getByRole('button',{name:'Cancel password reset',exact:true}).click();
    dialog = page.getByRole('dialog',{name:'Cancel password reset?'});
    await dialog.getByRole('button',{name:'Cancel reset',exact:true}).click();
    await expect(page.getByRole('button',{name:'Reset password',exact:true})).toBeEnabled();
    await page.getByRole('button',{name:'Delete user',exact:true}).click();
    dialog = page.getByRole('dialog',{name:`Delete ${name}?`});
    await dialog.getByRole('button',{name:'Delete user',exact:true}).click();
    await expect(page).toHaveURL(/\/settings\/users$/);
    expect((await api(page,'GET',`/api/v1/users/${userPath.split('/').at(-1)}`)).status).toBe(404);
  } finally { await person.close(); }
});

test('an administrator can keep and redeem their own reset link after signing out',async({page,browser})=>{
  const email=`${unique('self-reset')}@example.test`,password='A private fixture password for this admin 61';
  const created=await expectOk(api(page,'POST','/api/v1/users',{full_name:'Self Reset Admin',email,role:'administrator'}));
  const person=await browser.newContext({ignoreHTTPSErrors:true,storageState:{cookies:[],origins:[]}});
  try{
    const user=await person.newPage();
    await user.goto(created.body.link);
    await user.getByLabel('Password',{exact:true}).fill(password);
    await user.getByLabel('Confirm password',{exact:true}).fill(password);
    await user.getByRole('button',{name:'Set password',exact:true}).click();
    await expect(user.getByRole('heading',{name:'Your account is ready',exact:true})).toBeVisible();
    await user.getByRole('link',{name:'Continue to sign in',exact:true}).click();
    await user.getByLabel('Login',{exact:true}).fill(email);
    await user.getByLabel('Password',{exact:true}).fill(password);
    await user.getByRole('button',{name:'Sign in',exact:true}).click();
    await expect(user.getByRole('heading',{name:'Agents',exact:true})).toBeVisible();
    await user.goto(`/settings/users/${created.body.user.id}`);
    await user.getByRole('button',{name:'Reset password',exact:true}).click();
    await user.getByRole('dialog',{name:'Reset Self Reset Admin’s password?'}).getByRole('button',{name:'Generate link',exact:true}).click();
    const dialog=user.getByRole('dialog',{name:'Password reset link ready'});
    await expect(dialog).toBeVisible();
    await expect(user.locator('.application-shell')).toHaveCount(0);
    const link=(await dialog.locator('code').textContent())!;
    await expect(dialog.getByRole('button',{name:'Done',exact:true})).toBeFocused();
    await dialog.getByRole('button',{name:'Done',exact:true}).click();
    await expect(user.getByRole('button',{name:'Sign in',exact:true})).toBeVisible();
    await expect(user.locator('main h1')).toBeFocused();
    await expect(user.getByText(link,{exact:true})).toHaveCount(0);
    await user.goto(link);
    await expect(user.getByRole('heading',{name:'Reset your password',exact:true})).toBeVisible();
    await user.getByLabel('Password',{exact:true}).fill('Another private password for this admin 62');
    await user.getByLabel('Confirm password',{exact:true}).fill('Another private password for this admin 62');
    await user.getByRole('button',{name:'Set password',exact:true}).click();
    await expect(user.getByRole('heading',{name:'Your account is ready',exact:true})).toBeVisible();
  }finally{await person.close();}
});

test('an administrator saves the session inactivity timeout', async ({ page }) => {
  await page.goto('/settings/account');
  const sessions = page.getByRole('link', { name: 'Sessions', exact: true });
  await sessions.focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/settings\/sessions$/);
  const timeout = page.getByRole('spinbutton', { name: 'Inactivity timeout (minutes)' });
  await expect(timeout).toHaveValue('480');
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Your account', exact: true })).toBeVisible();
  await page.goForward();
  await expect(timeout).toHaveValue('480');
  await timeout.fill('300');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(timeout).toHaveValue('480');
  const path = '/api/v1/settings/sessions';
  const initial = (await expectOk(api(page, 'GET', path))).body;
  expect((await api(page, 'PUT', path, { idle_minutes: 721, version: initial.version })).status).toBe(400);
  try {
    await timeout.fill('360');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Session settings saved.' })).toBeVisible();
    expect((await api(page, 'PUT', path, { idle_minutes: 120, version: initial.version })).status).toBe(409);
    await page.reload();
    await expect(timeout).toHaveValue('360');
    await expect(page.getByText('Sessions end after 12 hours even with activity.', { exact: true })).toBeVisible();
  } finally {
    const current = (await expectOk(api(page, 'GET', path))).body;
    await expectOk(api(page, 'PUT', path, { idle_minutes: 480, version: current.version }));
  }
});
