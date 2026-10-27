import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, X509Certificate } from 'node:crypto';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { resolve, join, dirname } from 'node:path';
import { createServer } from 'node:net';
import { get } from 'node:https';
import { chromium, expect } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');
const fixtureMode = process.argv.length === 3 && process.argv[2] === '--cancellation-fixture';
if (process.argv.length !== 2 && !fixtureMode) throw new Error('Unsupported arguments');
mkdirSync(join(root, '.local'), { recursive: true });
const artifacts = mkdtempSync(join(root, '.local/compose-check-'));
const buildContext = mkdtempSync('/tmp/almanac-compose-source-');
function prepareBuildContext() {
  for (const name of ['.dockerignore', 'Dockerfile', 'Makefile', 'compose.yaml', 'go.mod', 'go.sum',
    'cmd', 'internal', 'deploy/local', 'web/assets.go', 'web/index.html', 'web/package.json',
    'web/package-lock.json', 'web/tsconfig.json', 'web/vite.config.ts', 'web/src', 'web/public']) {
    const destination = join(buildContext, name);
    const source = join(root, name);
    mkdirSync(dirname(destination), { recursive: true });
    cpSync(source, destination, { recursive: true, filter: (path) => {
      const nested = path.slice(source.length).replaceAll('\\', '/');
      return !/(?:^|\/)(?:\.env(?:\.[^/]*)?|\.agents|\.codex|\.aws|\.git|\.local|\.worktrees|node_modules)(?:\/|$)/.test(nested)
        && !/(?:^|\/)[^/]+\.(?:pem|key|secret|secrets)$/.test(nested);
    } });
  }
}
const sourceMarker = `<!-- compose source refresh ${randomBytes(12).toString('hex')} -->`;
const project = `almanac-check-${process.pid}-${randomBytes(4).toString('hex')}`;
const env = { ...process.env, COMPOSE_PROJECT_NAME: project, ALMANAC_PORT: '18086',
  ALMANAC_METRICS_PORT: '0', ALMANAC_TRACES_PORT: '0' };
delete env.COMPOSE_FILE;
const url = 'https://localhost:18086';
let stage = 'build and bootstrap';
let failed = false;
let cleanupFailed = false;
let initial;
let permanent;
let issuedToken;
let certificate;
let spki;
let lastFailure;
let cancellation;
let activeChild;
let activeBrowser;
let cleaning = false;
let cleanupPromise;

function redact(value) {
  let text = String(value ?? '');
  for (const secret of [initial, permanent, issuedToken]) if (secret) text = text.split(secret).join('[REDACTED]');
  return text.replace(/postgres(?:ql)?:\/\/[^\s"']+/g, '[DATABASE URL]')
    .replace(/\b[a-fA-F0-9]{64}\b/g, '[HEX SECRET OR DIGEST]')
    .replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----/g, '[PRIVATE KEY]')
    .slice(-64 * 1024);
}

function abortChild(child, signal) {
  if (!child?.pid) return;
  try {
    if (process.platform === 'win32') child.kill('SIGTERM');
    else process.kill(-child.pid, signal);
  } catch { /* The child may have exited between inspection and signalling. */ }
  const timer = setTimeout(() => {
    try {
      if (process.platform === 'win32') child.kill('SIGKILL');
      else process.kill(-child.pid, 'SIGKILL');
    } catch { /* Already gone. */ }
  }, 3000);
  timer.unref();
}

function onSignal(signal) {
  if (cancellation) return;
  cancellation = signal;
  if (cleaning) return;
  abortChild(activeChild, 'SIGTERM');
  void cleanup();
}
process.on('SIGINT', () => onSignal('SIGINT'));
process.on('SIGTERM', () => onSignal('SIGTERM'));
function ensureActive() {
  if (cancellation) throw Object.assign(new Error('cancelled'), { code: 'CANCELLED' });
}

function command(executable, args, { input, timeout = 120_000, environment = env, allowDuringCancellation = false, cwd = root } = {}) {
  if (!allowDuringCancellation) ensureActive();
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, args, { cwd, env: environment,
      detached: process.platform !== 'win32', stdio: ['pipe', 'pipe', 'pipe'] });
    activeChild = child;
    let overflow = false;
    let timedOut = false;
    const maxBuffer = 32 * 1024 * 1024;
    const collect = (target, chunk) => {
      const next = target.value + chunk.toString('utf8');
      if (Buffer.byteLength(next) > maxBuffer) {
        overflow = true;
        abortChild(child, 'SIGTERM');
      } else target.value = next;
    };
    const out = { value: '' };
    const err = { value: '' };
    child.stdout.on('data', (chunk) => collect(out, chunk));
    child.stderr.on('data', (chunk) => collect(err, chunk));
    child.stdin.on('error', () => {});
    child.on('error', (error) => {
      if (activeChild === child) activeChild = undefined;
      lastFailure = { executable, args, code: error.code, stdout: redact(out.value), stderr: redact(err.value) };
      rejectCommand(error);
    });
    if (input !== undefined) child.stdin.end(input);
    else child.stdin.end();
    const timer = setTimeout(() => { timedOut = true; abortChild(child, 'SIGTERM'); }, timeout);
    child.once('close', (status, signal) => {
      clearTimeout(timer);
      if (activeChild === child) activeChild = undefined;
      if (cancellation && !allowDuringCancellation) return rejectCommand(Object.assign(new Error('cancelled'), { code: 'CANCELLED' }));
      if (timedOut || overflow || status !== 0) {
        const error = Object.assign(new Error('command failed'), { status, signal, code: overflow ? 'MAXBUFFER' : timedOut ? 'TIMEOUT' : undefined });
        lastFailure = { executable, args, status, signal, code: error.code, stdout: redact(out.value), stderr: redact(err.value) };
        rejectCommand(error);
      } else resolveCommand(out.value);
    });
  });
}
function compose(args) { return command('docker', ['compose', '-f', 'compose.yaml', '-p', project, ...args]); }
async function checkMake(target) {
  const output = await command('make', [target], { timeout: 600_000, cwd: buildContext });
  assert.ok(!initial || !output.includes(initial));
  assert.ok(!permanent || !output.includes(permanent));
  writeFileSync(join(artifacts, `${target}-${Date.now()}.txt`), output, { mode: 0o600 });
  return output;
}
function servedHTML() {
  return new Promise((resolveHTML, rejectHTML) => {
    let settled = false;
    const timer = setTimeout(() => request.destroy(new Error('served HTML request timed out')), 10_000);
    const finish = (error, body) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (error) rejectHTML(error);
      else resolveHTML(body);
    };
    const request = get(url, { rejectUnauthorized: false }, (response) => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', (chunk) => { body += chunk; });
      response.on('error', finish);
      response.on('aborted', () => finish(new Error('served HTML response aborted')));
      response.on('end', () => response.statusCode === 200
        ? finish(undefined, body) : finish(new Error(`unexpected HTTP ${response.statusCode}`)));
    });
    request.on('error', finish);
  });
}
async function secret(name) { return compose(['exec', '-T', 'app', 'cat', `/run/almanac-secrets/${name}`]); }
async function databaseState() {
  return compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac',
    '-Atc', 'SELECT installation_id, bootstrap_admin_id, onboarding_completed_at IS NOT NULL FROM almanac_installation']);
}
async function secretDigest() {
  return createHash('sha256').update(await secret('database-url')).update(await secret('initial-admin-password'))
    .update(await secret('tls.crt')).update(await secret('tls.key'))
    .update(await secret('source-secret-keyset.json')).digest('hex');
}
async function checkPermanentLogin() {
  const browser = await chromium.launch({ args: [`--ignore-certificate-errors-spki-list=${spki}`] });
  if (cancellation) { await browser.close(); ensureActive(); }
  activeBrowser = browser;
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    await page.goto(url);
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(permanent);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
    await expect(page).toHaveURL(/\/agents(?:\?|$)/);
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  } finally {
    activeBrowser = undefined;
    await browser.close();
  }
}
async function setCaptureViewport(page, theme, viewport) {
  const { width, height, name } = viewport;
  await page.setViewportSize({ width, height });
  const wantsDark = theme === 'dark';
  const isDark = await page.locator('html').evaluate((element) => element.classList.contains('dark'));
  if (isDark !== wantsDark) {
    if (width <= 860) {
      await page.getByRole('button', { name: 'Menu', exact: true }).click();
      const drawer = page.getByRole('dialog', { name: 'Navigation menu', exact: true });
      await drawer.getByRole('button', { name: wantsDark ? 'Switch to dark theme' : 'Switch to light theme', exact: true }).click();
      await page.keyboard.press('Escape');
    } else {
      await page.getByRole('button', { name: wantsDark ? 'Switch to dark theme' : 'Switch to light theme', exact: true }).click();
    }
  }
  await expect.poll(() => page.locator('html').evaluate((element) => element.classList.contains('dark'))).toBe(wantsDark);
  return name;
}
async function captureMobileNavigation(page, theme, artifacts) {
  const menu = page.getByRole('button', { name: 'Menu', exact: true });
  await menu.click();
  const drawer = page.getByRole('dialog', { name: 'Navigation menu', exact: true });
  const close = drawer.getByRole('button', { name: 'Close navigation', exact: true });
  await expect(close).toBeFocused();
  await page.screenshot({ path: join(artifacts, `application-navigation-mobile-${theme}.png`), animations: 'disabled' });
  await close.focus();
  await page.keyboard.press('Shift+Tab');
  await expect(drawer.getByRole('button', { name: 'Sign out', exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(menu).toBeFocused();
  await expect(page.getByRole('dialog', { name: 'Navigation menu', exact: true })).toHaveCount(0);
}
async function checkLocalAgentRegistration() {
  const browser = await chromium.launch({ args: [`--ignore-certificate-errors-spki-list=${spki}`] });
  if (cancellation) { await browser.close(); ensureActive(); }
  activeBrowser = browser;
  let journeyPage;
  let documentMarker;
  const requests = [];
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    journeyPage = page;
    page.setDefaultTimeout(5000);
    let documentNavigations = 0;
    let contextRequests = 0;
    page.on('request', (incoming) => {
      if (incoming.isNavigationRequest() && incoming.frame() === page.mainFrame()) documentNavigations++;
      if (new URL(incoming.url()).pathname === '/api/v1/auth/context') contextRequests++;
      if (incoming.method() === 'POST' && new URL(incoming.url()).pathname === '/api/v1/agents') {
        requests.push(incoming.postDataJSON());
      }
    });
    await page.goto(url);
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(permanent);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
    stage = 'real Compose post-login Agents route';
    documentNavigations = 0;
    contextRequests = 0;
    documentMarker = await page.evaluate(() => {
      const marker = crypto.randomUUID();
      document.documentElement.dataset.composeDocumentMarker = marker;
      return marker;
    });
    const endpointPresentations = [];
    const endpointCaptures = [];
    const registrationCaptures = [];
    const datasourceCaptures = [];
    const detailCaptures = [];
    const viewports = [
      { name: 'desktop', width: 1440, height: 1000 },
      { name: 'mobile', width: 360, height: 900 },
    ];
    for (const theme of ['light', 'dark']) {
      for (const viewport of viewports) {
        const viewportName = await setCaptureViewport(page, theme, viewport);
        if (viewportName === 'mobile') await captureMobileNavigation(page, theme, artifacts);
        stage = `real Compose ${theme} ${viewportName} Agents to Datasources navigation`;
        await page.getByRole('link', { name: 'Datasources', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Datasources', exact: true })).toBeVisible();
        stage = `real Compose ${theme} ${viewportName} Add datasource navigation`;
        await page.getByRole('button', { name: 'Add datasource', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Add datasource', exact: true })).toBeVisible();
        stage = `real Compose ${theme} ${viewportName} datasource endpoint literal input`;
        const endpoint = page.locator('#datasource-endpoint');
        await endpoint.fill('http://localhost');
        const endpointPresentation = await endpoint.evaluate((element) => {
          const style = getComputedStyle(element);
          return {
            value: element.value,
            font_family: style.fontFamily,
            font_feature_settings: style.fontFeatureSettings,
            font_variant_ligatures: style.fontVariantLigatures,
          };
        });
        assert.equal(endpointPresentation.value, 'http://localhost');
        assert.equal(endpointPresentation.font_variant_ligatures, 'none');
        endpointPresentations.push({ theme, viewport: viewportName, ...endpointPresentation });
        const endpointCapture = `datasource-endpoint-${viewportName}-${theme}.png`;
        await endpoint.screenshot({ path: join(artifacts, endpointCapture), animations: 'disabled' });
        endpointCaptures.push(endpointCapture);
        await page.screenshot({
          path: join(artifacts, `datasource-add-${viewportName}-${theme}.png`),
          fullPage: true, animations: 'disabled',
        });
        datasourceCaptures.push(`datasource-add-${viewportName}-${theme}.png`);
        stage = `real Compose ${theme} ${viewportName} datasource back navigation`;
        await page.getByRole('button', { name: 'Back to datasources', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Datasources', exact: true })).toBeVisible();
        stage = `real Compose ${theme} ${viewportName} Datasources to Agents navigation`;
        await page.getByRole('link', { name: 'Agents', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
        stage = `real Compose ${theme} ${viewportName} Local name suggestion`;
        await page.getByRole('button', { name: 'Register agent', exact: true }).click();
        const registration = page.getByRole('dialog', { name: 'Register agent', exact: true });
        await registration.getByLabel('Display name', { exact: true }).fill('Local');
        const identifier = registration.getByLabel('Agent name (identifier)', { exact: true });
        await expect(identifier).toHaveValue('local');
        const registrationCapture = `local-agent-suggested-${viewportName}-${theme}.png`;
        await page.screenshot({ path: join(artifacts, registrationCapture), fullPage: true, animations: 'disabled' });
        registrationCaptures.push(registrationCapture);
        await registration.getByRole('button', { name: 'Close dialog', exact: true }).click();
        await expect(page.getByRole('dialog', { name: 'Register agent', exact: true })).toHaveCount(0);
      }
    }
    stage = 'real Compose same-document and session continuity assertions';
    assert.equal(await page.locator('html').getAttribute('data-compose-document-marker'), documentMarker);
    assert.equal(documentNavigations, 0);
    assert.equal(contextRequests, 0);
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toHaveCount(0);
    await setCaptureViewport(page, 'light', viewports[0]);
    stage = 'real Compose submit Local registration form';
    await page.getByRole('button', { name: 'Register agent', exact: true }).click();
    const registration = page.getByRole('dialog', { name: 'Register agent', exact: true });
    await registration.getByLabel('Display name', { exact: true }).fill('Local');
    const identifier = registration.getByLabel('Agent name (identifier)', { exact: true });
    await expect(identifier).toHaveValue('local');
    const contextRequestsBeforeRegistration = contextRequests;
    stage = 'real Compose submit Local registration form';
    const created = page.waitForResponse((response) => response.request().method() === 'POST'
      && new URL(response.url()).pathname === '/api/v1/agents');
    await registration.getByRole('button', { name: 'Register agent', exact: true }).click();
    const response = await created;
    stage = 'real Compose validate registration response and one-time token';
    assert.equal(response.status(), 201);
    const result = await response.json();
    issuedToken = result.token;
    assert.equal(result.agent.name, 'local');
    assert.equal(result.agent.display_name, 'Local');
    assert.match(result.token, /^almanac_agent_v1_[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$/);
    await expect(page.getByRole('dialog', { name: 'Agent token', exact: true })).toBeVisible();
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveText(result.token);
    assert.deepEqual(requests, [{ name: 'local', display_name: 'Local' }]);
    stage = 'real Compose close one-time token dialog';
    await page.getByRole('button', { name: 'Close token', exact: true }).click();
    stage = 'real Compose verify agent detail URL after token close';
    await expect(page).toHaveURL((current) => current.pathname === '/agents' && current.searchParams.has('agent'));
    stage = 'real Compose verify credential history after token close';
    await expect(page.getByRole('region', { name: 'Credential history', exact: true })).toBeVisible();
    stage = 'real Compose verify Local display name after token close';
    await expect(page.getByText('Local', { exact: true })).toBeVisible();
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    const agentURL = page.url();
    const registrationAuthContextRequests = contextRequests - contextRequestsBeforeRegistration;
    assert.equal(registrationAuthContextRequests, 1);
    for (const theme of ['light', 'dark']) {
      for (const viewport of viewports) {
        const viewportName = await setCaptureViewport(page, theme, viewport);
        stage = `real Compose ${theme} ${viewportName} agent detail screenshot after token closure`;
        await expect(page.getByRole('region', { name: 'Credential history', exact: true })).toBeVisible();
        await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
        const detailCapture = `agent-detail-after-token-${viewportName}-${theme}.png`;
        await page.screenshot({ path: join(artifacts, detailCapture), fullPage: true, animations: 'disabled' });
        detailCaptures.push(detailCapture);
      }
    }
    stage = 'real Compose detail to list browser back';
    await page.goBack();
    await expect(page).toHaveURL((current) => current.pathname === '/agents' && !current.searchParams.has('agent'));
    await expect(page.getByRole('button', { name: 'local', exact: true })).toBeVisible();
    stage = 'real Compose list to detail browser forward';
    await page.goForward();
    await expect(page).toHaveURL(agentURL);
    await expect(page.getByRole('region', { name: 'Credential history', exact: true })).toBeVisible();
    const preReloadCounts = { documentNavigations, contextRequests };
    const routeOnlyContinuity = {
      document_navigation_requests: preReloadCounts.documentNavigations,
      auth_context_requests: preReloadCounts.contextRequests - registrationAuthContextRequests,
    };
    assert.equal(routeOnlyContinuity.document_navigation_requests, 0);
    assert.equal(routeOnlyContinuity.auth_context_requests, 0);
    stage = 'real Compose reload persisted agent and one-time-token absence';
    await page.reload();
    await expect(page.getByText('local', { exact: true })).toBeVisible();
    await expect(page.getByText('Local', { exact: true })).toBeVisible();
    assert.equal(await page.locator('output[aria-label="Agent token"]').count(), 0);
    stage = 'real Compose verify PostgreSQL persistence';
    const persisted = await compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac', '-Atc',
      "SELECT count(*) FROM public.almanac_agents WHERE name='local' AND display_name='Local'"]);
    assert.equal(persisted.trim(), '1');
    const intentionalReloadCounts = {
      document_navigation_requests: documentNavigations - preReloadCounts.documentNavigations,
      auth_context_requests: contextRequests - preReloadCounts.contextRequests,
    };
    assert.equal(intentionalReloadCounts.document_navigation_requests, 1);
    assert.equal(intentionalReloadCounts.auth_context_requests, 1);
    writeFileSync(join(artifacts, 'local-agent-registration.json'), JSON.stringify({
      name: 'local', display_name: 'Local', create_requests: requests.length,
      token_shown_once: true, persisted: true,
      route_only_continuity: routeOnlyContinuity,
      registration_authority_context_requests: registrationAuthContextRequests,
      intentional_reload: intentionalReloadCounts,
      endpoint_presentations: endpointPresentations,
      screenshots: {
        datasource_add: datasourceCaptures,
        datasource_endpoint_literal: endpointCaptures,
        local_registration_presubmit: registrationCaptures,
        mobile_navigation: ['application-navigation-mobile-light.png', 'application-navigation-mobile-dark.png'],
        agent_detail_after_token_closed: detailCaptures,
      },
    }), { mode: 0o600 });
  } catch (error) {
    if (journeyPage) {
      const visible = async (locator) => { try { return await locator.isVisible(); } catch { return false; } };
      const pathname = (() => { try { return new URL(journeyPage.url()).pathname; } catch { return 'unavailable'; } })();
      const safeState = {
        stage,
        failure_type: error?.name ?? 'Error',
        pathname,
        post_request_created: requests.length > 0,
        document_marker_matches: documentMarker
          ? await journeyPage.locator('html').getAttribute('data-compose-document-marker') === documentMarker
          : false,
        registration_dialog_visible: await visible(journeyPage.getByRole('dialog', { name: 'Register agent', exact: true })),
        token_dialog_visible: await visible(journeyPage.getByRole('dialog', { name: 'Agent token', exact: true })),
        token_output_present: await journeyPage.locator('output[aria-label="Agent token"]').count() > 0,
        credential_history_visible: await visible(journeyPage.getByRole('region', { name: 'Credential history', exact: true })),
        local_display_name_visible: await visible(journeyPage.getByText('Local', { exact: true })),
      };
      writeFileSync(join(artifacts, 'local-agent-post-create-diagnostic.json'), JSON.stringify(safeState, null, 2), { mode: 0o600 });
    }
    throw error;
  } finally {
    activeBrowser = undefined;
    await browser.close();
  }
}
async function imageExists() {
  try { await command('docker', ['image', 'inspect', `${project}:dev`], { timeout: 20_000, allowDuringCancellation: true }); return true; }
  catch (error) { if (lastFailure?.status === 1) { lastFailure = undefined; return false; } throw error; }
}
function cleanup() {
  if (cleanupPromise) return cleanupPromise;
  cleaning = true;
  cleanupPromise = (async () => {
    if (activeChild) {
      abortChild(activeChild, 'SIGTERM');
      await new Promise((resolveClose) => activeChild?.once('close', resolveClose));
    }
    if (activeBrowser) {
      const browser = activeBrowser;
      activeBrowser = undefined;
      try { await browser.close(); } catch { cleanupFailed = true; console.error('Could not close acceptance browser.'); }
    }
    try {
      await command('docker', ['compose', '-f', 'compose.yaml', '-p', project,
        'down', '--volumes', '--remove-orphans', '--timeout', '35'], { timeout: 60_000, environment: env, allowDuringCancellation: true });
      if (await imageExists()) await command('docker', ['image', 'rm', `${project}:dev`], { timeout: 20_000, allowDuringCancellation: true });
      writeFileSync(join(artifacts, 'cleanup.json'), JSON.stringify({ containersVolumesNetworks: 'removed', image: 'removed if present' }), { mode: 0o600 });
    } catch {
      cleanupFailed = true;
      writeFileSync(join(artifacts, 'cleanup.json'), JSON.stringify({ containersVolumesNetworks: 'failed or incomplete', image: 'not confirmed removed' }), { mode: 0o600 });
      console.error(`Could not clean owned acceptance project ${project}; inspect it before rerunning.`);
    }
  })();
  return cleanupPromise;
}

try {
  stage = 'prepare isolated source context';
  prepareBuildContext();
  if (fixtureMode) console.log(JSON.stringify({ owned: true, project, artifacts }));
  if (fixtureMode) {
    stage = 'cancellation fixture bootstrap';
    await checkMake('bootstrap');
    // Keep a real child command active when the regression sends its signal.
    const pending = command(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { timeout: 600_000 });
    console.log(JSON.stringify({ ready: true, project, artifacts }));
    await pending;
    throw Object.assign(new Error('cancelled'), { code: 'CANCELLED' });
  }
  stage = 'fresh checkout starts with make run';
  const firstRun = await command('make', ['run'], { timeout: 600_000, cwd: buildContext });
  writeFileSync(join(artifacts, 'fresh-run-build.txt'), firstRun, { mode: 0o600 });
  stage = 'changed source is served after make start';
  const index = join(buildContext, 'web/index.html');
  writeFileSync(index, `${readFileSync(index, 'utf8')}\n${sourceMarker}\n`);
  await command('make', ['start'], { timeout: 600_000, cwd: buildContext });
  assert.ok((await servedHTML()).includes(sourceMarker), 'make start should serve assets built from changed source');
  stage = 'unchanged source uses Docker build cache';
  const unchangedRun = await command('make', ['run'], { timeout: 600_000, cwd: buildContext });
  assert.match(unchangedRun, /CACHED|Using cache/i);
  stage = 'build failure leaves the healthy app serving the old image';
  const dockerfile = readFileSync(join(buildContext, 'Dockerfile'), 'utf8');
  try {
    writeFileSync(join(buildContext, 'Dockerfile'), `${dockerfile}\nINVALID STARTUP TEST INSTRUCTION\n`);
    await assert.rejects(command('make', ['run'], { timeout: 600_000, cwd: buildContext }));
    assert.ok(!`${lastFailure.stdout}\n${lastFailure.stderr}`.includes('Almanac is ready'));
    writeFileSync(join(artifacts, 'failed-build.json'), JSON.stringify({ status: lastFailure.status,
      stderr: redact(lastFailure.stderr) }, null, 2), { mode: 0o600 });
    lastFailure = undefined;
  } finally { writeFileSync(join(buildContext, 'Dockerfile'), dockerfile); }
  assert.ok((await servedHTML()).includes(sourceMarker), 'build failure should leave the existing app available');
  stage = 'bootstrap performs one cached build';
  const bootstrap = await checkMake('bootstrap');
  assert.match(bootstrap, /CACHED|Using cache/i);
  assert.equal((bootstrap.match(/\[internal\] load build definition from Dockerfile/g) ?? []).length, 1,
    'make bootstrap should invoke one Compose build');
  stage = 'focused initializer checks';
  const checks = await compose(['run', '--rm', '--no-deps', '--entrypoint', 'sh', 'init', '/usr/local/lib/almanac/init-test.sh']);
  assert.match(checks, /PASS/);
  stage = 'inspect isolated runtime';
  const config = JSON.parse(await compose(['config', '--format', 'json']));
  assert.equal(config.services.app.user, '10001:10001');
  assert.equal(config.services.app.read_only, true);
  assert.ok(!config.services.postgres.ports?.length);
  assert.equal(config.services.app.ports[0].host_ip, '127.0.0.1');
  assert.equal(String(config.services.app.ports[0].published), '18086');
  initial = await secret('initial-admin-password');
  assert.ok(initial.length >= 32 && !initial.includes('\n'));
  certificate = await secret('tls.crt');
  const publicKey = new X509Certificate(certificate).publicKey.export({ type: 'spki', format: 'der' });
  spki = createHash('sha256').update(publicKey).digest('base64');
  const beforeSecrets = await secretDigest();
  const postgresID = (await compose(['ps', '-q', 'postgres'])).trim();
  permanent = `Compose permanent ${randomBytes(20).toString('hex')}`;
  stage = 'real browser onboarding';
  const result = await command(process.execPath, [join(root, 'web/scripts/check-auth-browser.mjs')], {
    input: JSON.stringify({ scenario: 'onboarding', url, login: 'admin', initial, permanent, certificate, spki, artifacts }), timeout: 60_000 });
  assert.match(result, /PASS browser-auth onboarding/);
  const completed = await databaseState();
  assert.match(completed.trim(), /\|t$/);
  stage = 'invalid port fails before changing running stack';
  await assert.rejects(command('make', ['run'], { cwd: buildContext, environment: { ...env, ALMANAC_PORT: 'invalid' } }));
  assert.match(lastFailure.stderr, /ALMANAC_PORT must/);
  writeFileSync(join(artifacts, 'invalid-port.json'), JSON.stringify(lastFailure), { mode: 0o600 });
  lastFailure = undefined;
  assert.equal(await databaseState(), completed);
  stage = 'stop and daily start preserve onboarding';
  await checkMake('stop');
  stage = 'occupied port fails without false readiness';
  const occupied = createServer();
  await new Promise((resolveListen, reject) => { occupied.once('error', reject); occupied.listen(18086, '127.0.0.1', resolveListen); });
  try {
    await assert.rejects(command('make', ['run'], { cwd: buildContext, timeout: 600_000 }));
    assert.match(`${lastFailure.stdout}\n${lastFailure.stderr}`, /(?:address already in use|port is already allocated|failed to bind host port)/i);
    assert.ok(!lastFailure.stdout.includes('Almanac is ready'));
    writeFileSync(join(artifacts, 'occupied-port.json'), JSON.stringify(lastFailure), { mode: 0o600 });
    lastFailure = undefined;
  } finally { await new Promise((resolveClose) => occupied.close(resolveClose)); }
  stage = 'daily start after port released';
  await checkMake('run');
  assert.equal((await compose(['ps', '-q', 'postgres'])).trim(), postgresID);
  assert.equal(await secretDigest(), beforeSecrets);
  assert.equal(await databaseState(), completed);
  await checkPermanentLogin();
  stage = 'repeat bootstrap preserves onboarding';
  await checkMake('bootstrap');
  assert.equal((await compose(['ps', '-q', 'postgres'])).trim(), postgresID);
  assert.equal(await secretDigest(), beforeSecrets);
  assert.equal(await databaseState(), completed);
  await checkPermanentLogin();
  stage = 'real Compose browser registration with Local display name';
  await checkLocalAgentRegistration();
  stage = 'logs exclude credentials';
  const logs = await compose(['logs', '--no-color']);
  assert.ok(!logs.includes(initial));
  assert.ok(!logs.includes(permanent));
  const dbPassword = await compose(['exec', '-T', 'postgres', 'cat', '/run/postgres-secret/password']);
  assert.ok(dbPassword.length >= 32 && !logs.includes(dbPassword));
  writeFileSync(join(artifacts, 'acceptance.json'), JSON.stringify({ project,
    revision: (await command('git', ['rev-parse', 'HEAD'])).trim(), completed: true,
    harnessSHA256: Object.fromEntries(['check-local-compose.mjs', 'check-local-compose-cancellation.mjs']
      .map((name) => [name, createHash('sha256').update(readFileSync(join(root, 'web/scripts', name))).digest('hex')])),
    candidateDiffSHA256: createHash('sha256').update(await command('git', ['diff', 'HEAD', '--',
      'Dockerfile', '.dockerignore', 'compose.yaml', 'Makefile', 'deploy/local', 'web/scripts/check-local-compose.mjs'])).digest('hex'),
    checks: ['initializer boundaries', 'real browser onboarding', 'stop/run persistence', 'repeat bootstrap persistence',
      'real browser agent registration with visible Local identifier suggestion and one-time token',
      'nonroot/read-only/loopback', 'secret-free logs', 'invalid port refusal', 'occupied port refusal'],
  }, null, 2), { mode: 0o600 });
} catch {
  failed = true;
  if (cancellation) stage = `cancelled by ${cancellation}`;
  const diagnostics = { stage, lastFailure };
  if (!cancellation) {
    try { diagnostics.services = redact(await compose(['ps', '-a', '--format', 'json'])); } catch { diagnostics.services = 'unavailable'; }
    try {
      const appID = (await compose(['ps', '-aq', 'app'])).trim();
      if (/^[a-f0-9]+$/.test(appID)) diagnostics.appRuntime = redact(await command('docker', ['inspect',
        '--format', '{"state":{{json .State}},"networks":{{json .NetworkSettings.Networks}},"networkMode":{{json .HostConfig.NetworkMode}}}', appID]));
    } catch { diagnostics.appRuntime = 'unavailable'; }
    try { diagnostics.logs = redact(await compose(['logs', '--no-color', '--tail', '80'])); } catch { diagnostics.logs = 'unavailable'; }
  }
  writeFileSync(join(artifacts, 'diagnostics.json'), JSON.stringify(diagnostics, null, 2), { mode: 0o600 });
  writeFileSync(join(artifacts, 'compose-failure.txt'), `Failed stage: ${stage}\n`, { mode: 0o600 });
  if (!cancellation) console.error(`FAIL local Compose at ${stage}; sanitized evidence: ${artifacts}`);
} finally { await cleanup(); rmSync(buildContext, { recursive: true, force: true }); }

if (cancellation) process.exitCode = cancellation === 'SIGINT' ? 130 : 143;
else if (failed || cleanupFailed) process.exitCode = 1;
else console.log(`PASS local Compose startup, onboarding, repeat bootstrap and restart; evidence: ${artifacts}`);
