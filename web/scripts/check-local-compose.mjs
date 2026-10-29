import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, X509Certificate } from 'node:crypto';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { resolve, join, dirname } from 'node:path';
import { createConnection, createServer } from 'node:net';
import { get, request } from 'node:https';
import { chromium, expect } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');
const fixtureMode = process.argv.length === 3 && process.argv[2] === '--cancellation-fixture';
if (process.argv.length !== 2 && !fixtureMode) throw new Error('Unsupported arguments');
mkdirSync(join(root, '.local'), { recursive: true });
const artifacts = mkdtempSync(join(root, '.local/compose-check-'));
const buildContext = mkdtempSync('/tmp/almanac-compose-source-');
function prepareBuildContext() {
  for (const name of ['.dockerignore', 'Dockerfile', 'Makefile', 'compose.yaml', 'go.mod', 'go.sum',
    'cmd', 'internal', 'deploy/local', 'scripts/check-local-readiness.sh', 'scripts/local-agent.sh', 'web/assets.go', 'web/index.html', 'web/package.json',
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
const launchers = [];
let launcherOutput = '';
let launcherLogs = '';
const relayContainer = `${project}-relay`;
const inputContainer = `${project}-input-init`;
const inputVolume = `${project}-agent-input`;
const nodeImage = 'node:26.8.1-bookworm-slim@sha256:367679cf9792759492a486e4aa4b421764d71a9546a6dae8aab81a99eb797b3e';
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
let replacementToken;
let invalidToken;
let certificate;
let spki;
let lastFailure;
let cancellation;
let activeChild;
let activeChildDone;
let activeBrowser;
let cleaning = false;
let cleanupPromise;

function redact(value) {
  let text = String(value ?? '');
  for (const secret of [initial, permanent, issuedToken, replacementToken, invalidToken]) if (secret) text = text.split(secret).join('[REDACTED]');
  return text.replace(/(["']?(?:set-)?cookie["']?\s*:\s*)[^\r\n]*/gi, '$1[REDACTED]')
    .replace(/postgres(?:ql)?:\/\/[^\s"']+/g, '[DATABASE URL]')
    .replace(/\b[a-fA-F0-9]{64}\b/g, '[HEX SECRET OR DIGEST]')
    .replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----/g, '[PRIVATE KEY]')
    .slice(-64 * 1024);
}
function containsFixtureSecret(value) {
  const text = String(value ?? '');
  return [initial, permanent, issuedToken, replacementToken, invalidToken].some((secret) => secret && text.includes(secret));
}

function failureCategory(error) {
  const message = typeof error?.message === 'string' ? error.message : '';
  if (/strict mode violation/i.test(message)) return 'strict-locator';
  if (error?.name === 'TimeoutError' || /timeout .* exceeded|timed out/i.test(message)) return 'timeout';
  if (error?.name === 'AssertionError' || error?.name === 'ExpectError' || /^expect\(/.test(message)) return 'assertion';
  return 'other';
}

async function withTrackedRoute(page, pattern, handle, exercise, release = () => {}) {
  const pending = new Set();
  const failures = [];
  const handler = (route) => {
    const task = Promise.resolve().then(() => handle(route)).catch((error) => { failures.push(error); });
    pending.add(task);
    void task.then(() => pending.delete(task));
    return task;
  };
  await page.route(pattern, handler);
  try {
    await exercise();
  } catch (error) {
    failures.push(error);
  } finally {
    release();
    try { await page.unroute(pattern, handler); } catch (error) { failures.push(error); }
    await Promise.all([...pending]);
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, `Route exercise failed (${failures.map(failureCategory).join(', ')})`);
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

function command(executable, args, { input, timeout = 120_000, environment = env, allowDuringCancellation = false, cwd = root, includeStderr = false } = {}) {
  if (!allowDuringCancellation) ensureActive();
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, args, { cwd, env: environment,
      detached: process.platform !== 'win32', stdio: ['pipe', 'pipe', 'pipe'] });
    activeChild = child;
    activeChildDone = new Promise((resolveClose) => child.once('close', resolveClose));
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
      lastFailure = { executable, args, code: error.code, secretLeak: containsFixtureSecret(out.value) || containsFixtureSecret(err.value), stdout: redact(out.value), stderr: redact(err.value) };
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
        lastFailure = { executable, args, status, signal, code: error.code, secretLeak: containsFixtureSecret(out.value) || containsFixtureSecret(err.value), stdout: redact(out.value), stderr: redact(err.value) };
        rejectCommand(error);
      } else resolveCommand(includeStderr ? out.value + err.value : out.value);
    });
  });
}
function compose(args) { return command('docker', ['compose', '-f', 'compose.yaml', '-p', project, ...args]); }
async function checkMake(target, environment = env) {
  const output = await command('make', [target], { timeout: 600_000, cwd: buildContext, environment });
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
function probeOccupiedPort() {
  return new Promise((resolveProbe) => {
    const socket = createConnection({ host: '127.0.0.1', port: 18086 });
    let received = '';
    let settled = false;
    const finish = (result) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      resolveProbe(result);
    };
    socket.setTimeout(2000, () => finish(false));
    socket.on('error', () => finish(false));
    socket.on('data', (chunk) => {
      received += chunk.toString('utf8');
      if (received.length > 128) finish(false);
      else if (received === 'almanac-owned-port-fixture\n') finish(true);
    });
    socket.on('end', () => finish(received === 'almanac-owned-port-fixture\n'));
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
async function navigateWorkspace(page, viewportName, destination) {
  let navigation;
  if (viewportName === 'mobile') {
    await page.getByRole('button', { name: 'Menu', exact: true }).click();
    const drawer = page.getByRole('dialog', { name: 'Navigation menu', exact: true });
    navigation = drawer.getByRole('navigation', { name: 'Primary navigation', exact: true });
  } else {
    navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
  }
  await navigation.getByRole('link', { name: destination, exact: true }).click();
}
async function startAgent(page, agentID, token) {
  const appID = (await compose(['ps', '-q', 'app'])).trim();
  assert.match(appID, /^[a-f0-9]+$/);
  stage = 'start isolated real agent with make agent';
  const handle = { appID, agentID, token, tokenFile: join(artifacts, `agent-token-${agentID}-${randomBytes(12).toString('hex')}`), child: null, done: null, exited: false, container: undefined, output: '' };
  launchers.push(handle);
  writeFileSync(handle.tokenFile, token, { mode: 0o600 });
  const before = new Set((await command('docker', ['ps', '-aq', '--filter', 'label=almanac.local-agent=owned', '--filter', `label=almanac.local-app=${appID}`])).trim().split('\n').filter(Boolean));
  handle.child = spawn('make', ['agent'], { cwd: root, env: { ...env, AGENT_TOKEN_FILE: handle.tokenFile }, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
  handle.child.stdout.on('data', (chunk) => { handle.output += chunk; launcherOutput += chunk; });
  handle.child.stderr.on('data', (chunk) => { handle.output += chunk; launcherOutput += chunk; });
  handle.done = new Promise((resolveClose) => {
    handle.child.once('error', (error) => { handle.exited = true; resolveClose({ error: error.code }); });
    handle.child.once('close', (code, signal) => { handle.exited = true; resolveClose({ code, signal }); });
  });
  await expect.poll(async () => {
    if (handle.exited) throw new Error(`launcher exited before startup: ${redact(handle.output)}`);
    const ids = (await command('docker', ['ps', '-aq', '--filter', 'label=almanac.local-agent=owned', '--filter', `label=almanac.local-app=${appID}`])).trim().split('\n').filter((id) => id && !before.has(id));
    if (ids.length === 1) {
      const info = JSON.parse(await command('docker', ['inspect', ids[0]]))[0];
      assert.equal(info.Config.Labels?.['almanac.local-agent'], 'owned');
      assert.equal(info.Config.Labels?.['almanac.local-app'], appID);
      assert.equal(info.HostConfig.NetworkMode, `container:${appID}`);
      handle.container = ids[0];
    }
    return ids.length;
  }, { timeout: 15_000 }).toBe(1);
  const agent = JSON.parse(await command('docker', ['inspect', handle.container]))[0];
  assert.equal(agent.HostConfig.NetworkMode, `container:${appID}`);
  assert.equal(agent.HostConfig.ReadonlyRootfs, true);
  assert.equal(agent.Config.User, '10001:10001');
  assert.equal(agent.Mounts.length, 0);
  assert.ok(Object.hasOwn(agent.HostConfig.Tmpfs, '/tmp'));
  assert.ok(!JSON.stringify(agent.Config).includes(token));
  stage = 'wait for assigned agent connection';
  await expect.poll(async () => page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/agents/${id}`);
    if (!response.ok) return '';
    const runtime = (await response.json()).runtime;
    return runtime?.reachability === 'connected' && runtime.capabilities?.some((capability) => capability.provider === 'prometheus' && capability.configuration_formats.includes('prometheus-http-v1') && capability.operations.includes('connection_test') && capability.operations.includes('query_preview')) ? 'connected' : '';
  }, agentID), { timeout: 25_000 }).toBe('connected');
  return handle;
}
async function stopAgent(handle) {
  launcherLogs += await command('docker', ['logs', handle.container], { includeStderr: true });
  stage = 'stop only the launched agent';
  if (!handle.exited) process.kill(-handle.child.pid, 'SIGINT');
  const stopped = await Promise.race([handle.done.then(() => true), new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(false), 5000))]);
  if (!stopped) { abortChild(handle.child, 'SIGTERM'); throw new Error('Acceptance launcher did not stop after interruption'); }
  assert.equal((await command('docker', ['ps', '-aq', '--filter', `id=${handle.container}`])).trim(), '');
  assert.equal((await compose(['ps', '-q', 'app'])).trim(), handle.appID);
  assert.ok(!handle.output.includes(handle.token));
  rmSync(handle.tokenFile); handle.tokenFile = undefined; handle.container = undefined;
}
async function checkRejectedAgentStarts(handle) {
  const containers = async () => (await command('docker', ['ps', '-aq', '--filter', 'label=almanac.local-agent=owned', '--filter', `label=almanac.local-app=${handle.appID}`])).trim().split('\n').filter(Boolean).sort();
  const temporaryInputs = () => readdirSync(join(root, '.local')).filter((name) => name.startsWith('almanac-agent.')).sort();
  const beforeContainers = await containers(); const beforeInputs = temporaryInputs();
  const dot = issuedToken.lastIndexOf('.');
  invalidToken = `${issuedToken.slice(0, dot + 1)}${issuedToken[dot + 1] === 'A' ? 'B' : 'A'}${issuedToken.slice(dot + 2)}`;
  for (const [name, token, expected] of [['invalid', invalidToken, /credential rejected/i]]) {
    stage = `real Compose rejected ${name} agent startup`;
    const file = join(artifacts, `${name}-agent-token`); writeFileSync(file, token, { mode: 0o600 });
    try {
      await assert.rejects(command('make', ['agent'], { environment: { ...env, AGENT_TOKEN_FILE: file }, timeout: 25_000 }));
      assert.ok(lastFailure && lastFailure.status !== 0 && !lastFailure.code, `${name} agent did not exit normally with failure`);
      assert.match(`${lastFailure.stdout}\n${lastFailure.stderr}`, expected);
      assert.equal(lastFailure.secretLeak, false, `${name} agent leaked a fixture secret`);
      lastFailure = undefined;
      assert.deepEqual(await containers(), beforeContainers); assert.deepEqual(temporaryInputs(), beforeInputs);
      assert.equal(handle.exited, false);
    } finally { rmSync(file, { force: true }); }
  }
}
async function checkAgentRestarts(page, handle, agentID, sliID, originalObservation) {
  const query = async (sql) => (await compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac', '-Atc', sql])).trim();
  const currentOwner = async () => JSON.parse(await query(`SELECT json_build_object('generation', generation, 'expires_at', expires_at, 'unexpired', expires_at > now()) FROM public.almanac_agent_ownership WHERE agent_id='${agentID}' ORDER BY generation DESC LIMIT 1`));
  const oldItem = JSON.parse(await query(`SELECT json_build_object(
    'delivery_id', c.delivery_id, 'execution_id', c.execution_id, 'created_at', c.agent_created_at,
    'lease_id', c.lease_id, 'configuration_revision', c.configuration_revision::text,
    'session_generation', c.session_generation::text, 'sli_id', c.sli_id,
    'measurement_revision', c.measurement_revision::text, 'interval_start', c.interval_start,
    'interval_end', c.interval_end, 'outcome', json_build_object('state', 'observed', 'good_count', v.good_count::text, 'total_count', v.total_count::text))
    FROM public.almanac_sli_coverage c JOIN public.almanac_sli_observation_values v USING (interval_start,sli_id,measurement_revision)
    WHERE c.sli_id='${sliID}' AND c.outcome='observed' ORDER BY c.interval_start DESC LIMIT 1`));
  const cursorBefore = await query(`SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id='${sliID}' AND revision=${oldItem.measurement_revision}`);
  const submit = () => new Promise((resolveSubmit, rejectSubmit) => {
    const body = JSON.stringify({ items: [oldItem] });
    const req = request(`${url}/api/agent/v1/observations`, { method: 'POST', ca: certificate,
      headers: { Authorization: `Bearer ${issuedToken}`, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) } }, (response) => {
      let data = '';
      response.setEncoding('utf8');
      response.on('data', (chunk) => { data += chunk; });
      response.on('end', () => { try { resolveSubmit({ status: response.statusCode, body: JSON.parse(data) }); } catch (error) { rejectSubmit(error); } });
    });
    req.setTimeout(15_000, () => req.destroy(new Error('Agent observation request timed out')));
    req.on('error', rejectSubmit);
    req.end(body);
  });
  stage = 'validate original agent receipt before replacement';
  const priorReceipt = await submit();
  assert.equal(priorReceipt.status, 200);
  assert.equal(priorReceipt.body.items[0].status, 'accepted');
  assert.ok(priorReceipt.body.items[0].observation_id);
  const restartEvidence = [];
  let agent = handle;
  for (const mode of ['clean', 'kill', 'backend']) {
    stage = `real Compose ${mode} agent restart before ownership expiry`;
    const previous = await currentOwner();
    assert.equal(previous.unexpired, true);
    const started = performance.now();
    if (mode === 'kill') {
      await command('docker', ['kill', '--signal', 'KILL', agent.container]);
      await Promise.race([agent.done, new Promise((_, reject) => setTimeout(() => reject(new Error('Killed agent launcher did not exit')), 15000))]);
      rmSync(agent.tokenFile); agent.tokenFile = undefined; agent.container = undefined;
    } else {
      await stopAgent(agent);
    }
    if (mode === 'backend') {
      await compose(['restart', 'app']);
      await expect.poll(async () => {
        try {
          const appID = (await compose(['ps', '-q', 'app'])).trim();
          const health = (await command('docker', ['inspect', '--format', '{{.State.Health.Status}}', appID])).trim();
          return health === 'healthy' && await page.evaluate(async () => (await fetch('/livez', { cache: 'no-store' })).ok);
        } catch { return false; }
      }, { timeout: 25_000 }).toBe(true);
    }
    agent = await startAgent(page, agentID, issuedToken);
    await expect.poll(async () => (await currentOwner()).generation, { timeout: 25_000 }).toBe(previous.generation + 1);
    const connectElapsedMS = Math.round(performance.now() - started);
    assert.equal(await query(`SELECT expires_at > now() FROM public.almanac_agent_ownership WHERE agent_id='${agentID}' AND generation=${previous.generation}`), 't');
    await expect.poll(async () => Number(await query(`SELECT count(*) FROM public.almanac_sli_coverage WHERE sli_id='${sliID}' AND session_generation=${previous.generation + 1} AND outcome='observed'`)), { timeout: 110_000, intervals: [1000] }).toBeGreaterThan(0);
    const replacementObservation = JSON.parse(await query(`SELECT json_build_object('observation_id', observation_id, 'interval_start', interval_start, 'interval_end', interval_end, 'recorded_at', recorded_at) FROM public.almanac_sli_coverage WHERE sli_id='${sliID}' AND session_generation=${previous.generation + 1} AND outcome='observed' ORDER BY interval_start DESC LIMIT 1`));
    restartEvidence.push({ mode, old_generation: previous.generation, new_generation: previous.generation + 1, old_expires_at: previous.expires_at, connect_elapsed_ms: connectElapsedMS, observation: replacementObservation });
    writeFileSync(join(artifacts, 'agent-restarts.json'), JSON.stringify(restartEvidence, null, 2), { mode: 0o600 });
  }
  stage = 'reject the former generation receipt and retain history';
  const receipt = await submit();
  assert.equal(receipt.status, 200);
  assert.equal(receipt.body.items[0].status, 'rejected');
  assert.equal(receipt.body.items[0].error_code, 'execution_not_authorized');
  const observations = await page.evaluate(async (id) => (await (await fetch(`/api/v1/slis/${id}/observations?limit=50`)).json()).items, sliID);
  assert.deepEqual(observations.find((item) => item.observation_id === originalObservation.observation_id), originalObservation);
  const cursorAfter = await query(`SELECT last_attempted_interval_end FROM public.almanac_sli_measurements WHERE sli_id='${sliID}' AND revision=${oldItem.measurement_revision}`);
  assert.ok(Date.parse(cursorAfter) >= Date.parse(cursorBefore));
  return agent;
}
async function checkCollectedSLI(page, agentID) {
  const originalAgent = await startAgent(page, agentID, issuedToken);
  const appID = originalAgent.appID;
  await checkRejectedAgentStarts(originalAgent);
  stage = 'replace a connected agent using the same credential';
  let oldAgent = await startAgent(page, agentID, issuedToken);
  const originalExit = await Promise.race([originalAgent.done, new Promise((_, reject) => setTimeout(() => reject(new Error('Replaced agent did not exit')), 15000))]);
  assert.equal(originalExit.code, 0);
  assert.match(originalAgent.output, /replaced by a newer process; stopping/i);
  assert.ok(!originalAgent.output.includes(issuedToken));
  rmSync(originalAgent.tokenFile); originalAgent.tokenFile = undefined;
  stage = 'real Compose SLI prerequisite and datasource creation';
  await page.goto(`${url}/slis`);
  await expect(page.getByText('Add a Prometheus datasource to create your first SLI.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Create SLI', exact: true })).toHaveCount(0);
  for (const theme of ['light', 'dark']) {
    for (const viewport of [{ name: 'desktop', width: 1440, height: 1000 }, { name: 'mobile', width: 360, height: 900 }]) {
      await setCaptureViewport(page, theme, viewport);
      await page.screenshot({ path: join(artifacts, `sli-prerequisite-${viewport.name}-${theme}.png`), fullPage: true, animations: 'disabled' });
    }
  }
  await setCaptureViewport(page, 'light', { name: 'desktop', width: 1440, height: 1000 });
  await page.getByRole('button', { name: 'Add datasource', exact: true }).click();
  await expect(page).toHaveURL(/\/datasources\/new\?return=slis$/);
  await page.getByLabel('Display name', { exact: true }).fill('Compose metrics');
  await page.getByLabel('Prometheus endpoint', { exact: true }).fill('http://victoriametrics:8428');
  await page.getByLabel('Assigned agent', { exact: true }).selectOption(agentID);
  await page.getByLabel('Polling interval (seconds)', { exact: true }).fill('15');
  const created = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/datasources');
  await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
  const response = await created;
  const datasource = { status: response.status(), body: await response.json() };
  assert.equal(datasource.status, 201);
  assert.match(datasource.body.id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.equal(datasource.body.polling_interval_seconds, 15);
  await expect(page).toHaveURL((current) => current.pathname === '/slis/new' && current.searchParams.get('datasource') === datasource.body.id);
  await expect(page.getByLabel('Datasource', { exact: true })).toHaveValue(datasource.body.id);
  await page.goBack();
  await expect(page).toHaveURL(/\/datasources\/new\?return=slis$/);
  await page.goForward();
  await expect(page.getByLabel('Datasource', { exact: true })).toHaveValue(datasource.body.id);
  await page.getByLabel('Display name', { exact: true }).fill('First journey');
  await expect(page.getByLabel('SLI name', { exact: true })).toHaveValue('first-journey');
  await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Enter a good count query');
  await page.getByLabel('Good count query', { exact: true }).fill('vector(99)');
  await page.getByLabel('Total count query', { exact: true }).fill('vector(100)');
  await page.screenshot({ path: join(artifacts, 'sli-return-from-datasource.png'), fullPage: true, animations: 'disabled' });
  const sliCreated = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/slis');
  await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
  const sliResponse = await sliCreated;
  assert.equal(sliResponse.status(), 201);
  const firstSLI = await sliResponse.json();
  assert.equal(firstSLI.datasource_id, datasource.body.id);
  await expect(page.getByRole('heading', { name: 'First journey', exact: true })).toBeVisible();
  await expect.poll(async () => {
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    const counts = page.locator('.sli-counts strong');
    return await counts.count() === 2 ? [await counts.nth(0).textContent(), await counts.nth(1).textContent()] : [];
  }, { timeout: 110_000, intervals: [1000] }).toEqual(['99', '100']);
  const firstDetail = await page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/slis/${id}`, { credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok) throw new Error('SLI detail unavailable');
    return response.json();
  }, firstSLI.id);
  const firstObservation = firstDetail.latest_observation;
  assert.equal(firstObservation.outcome, 'observed');
  assert.equal(Date.parse(firstObservation.interval_end) - Date.parse(firstObservation.interval_start), 15_000);
  assert.ok(Date.parse(firstObservation.interval_start) >= Date.parse(firstSLI.effective_from));
  assert.ok(Date.parse(firstObservation.recorded_at) >= Date.parse(firstObservation.interval_end) + 60_000);
  await page.screenshot({ path: join(artifacts, 'sli-first-journey-observed.png'), fullPage: true, animations: 'disabled' });
  stage = 'real Compose browser SLI creation and agent collection';
  const browserResult = await command(process.execPath, [join(root, 'web/scripts/check-sli-browser.mjs')], {
    input: JSON.stringify({ url, spki, login: 'admin', password: permanent, agent_id: agentID,
      datasource_id: datasource.body.id, artifacts }), timeout: 125_000 });
  assert.equal(browserResult.trim(), 'PASS browser-sli-journey');
  const evidence = JSON.parse(readFileSync(join(artifacts, 'sli-journey.json'), 'utf8'));
  assert.match(evidence.sli_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.equal(evidence.agent_id, agentID);
  assert.equal(evidence.datasource_id, datasource.body.id);
  assert.equal(evidence.outcome, 'observed');
  assert.equal(evidence.good_count, '99');
  assert.equal(evidence.total_count, '100');
  stage = 'real Compose first SLO journey';
  const sloBrowserResult = await command(process.execPath, [join(root, 'web/scripts/check-slo-browser.mjs')], {
    input: JSON.stringify({ url, spki, login: 'admin', password: permanent, sli_id: evidence.sli_id, artifacts }), timeout: 125_000 });
  assert.equal(sloBrowserResult.trim(), 'PASS browser-slo-journey');
  stage = 'capture observed SLI in both themes and mobile layout';
  await page.goto(`${url}/slis/${evidence.sli_id}`);
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).toBeVisible();
  for (const theme of ['light', 'dark']) {
    for (const viewport of [{ name: 'desktop', width: 1440, height: 1000 }, { name: 'mobile', width: 360, height: 900 }]) {
      await setCaptureViewport(page, theme, viewport);
      const counts = page.locator('.sli-counts strong');
      await expect(counts.nth(0)).toHaveText('99');
      await expect(counts.nth(1)).toHaveText('100');
      await page.screenshot({ path: join(artifacts, `compose-sli-${viewport.name}-${theme}.png`), fullPage: true, animations: 'disabled' });
    }
  }
  stage = 'verify persisted observation provenance and cadence';
  const persisted = await compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac', '-Atc',
    `SELECT v.good_count::text, v.total_count::text, c.outcome, c.agent_id::text, extract(epoch from (c.interval_end-c.interval_start))::integer FROM public.almanac_sli_coverage c JOIN public.almanac_sli_observation_values v USING (interval_start,sli_id,measurement_revision) WHERE c.sli_id='${evidence.sli_id}' ORDER BY c.interval_start DESC LIMIT 1`]);
  assert.equal(persisted.trim(), `99.000000000000|100.000000000000|observed|${agentID}|15`);
  const initialHistory = await page.evaluate(async (id) => (await (await fetch(`/api/v1/slis/${id}/observations?limit=50`)).json()).items, evidence.sli_id);
  const initialObservation = initialHistory.find((item) => item.outcome === 'observed' && item.good_count === '99' && item.total_count === '100');
  assert.ok(initialObservation);
  oldAgent = await checkAgentRestarts(page, oldAgent, agentID, evidence.sli_id, initialObservation);
  stage = 'start public-origin loopback relay for CLI';
  await command('docker', ['run', '-d', '--name', relayContainer, '--network', `container:${appID}`, '--read-only',
    '--entrypoint', 'node', nodeImage, '-e',
    "const net=require('net');net.createServer(a=>{const b=net.connect(8080,'127.0.0.1');a.on('error',()=>b.destroy());b.on('error',()=>a.destroy());a.pipe(b).pipe(a)}).listen(18086,'127.0.0.1')"]);
  await expect.poll(async () => {
    try {
      await compose(['exec', '-T', 'app', 'curl', '--fail', '--silent', '--cacert', '/run/almanac-secrets/tls.crt', `${url}/livez`]);
      return true;
    } catch { return false; }
  }, { timeout: 10_000 }).toBe(true);
  lastFailure = undefined;
  const cliDir = `/tmp/${project}-cli`;
  await compose(['exec', '-T', 'app', 'mkdir', '-m', '700', cliDir]);
  await command('docker', ['compose', '-f', 'compose.yaml', '-p', project, 'exec', '-T', 'app', 'sh', '-c',
    'umask 077; cat > "$1/password"', 'sh', cliDir], { input: permanent });
  const cli = (...args) => compose(['exec', '-T', 'app', '/usr/local/bin/almanac', ...args]);
  stage = 'authenticate CLI against configured public origin';
  await cli('auth', 'login', `--profile-file=${cliDir}/profile.json`, `--backend-url=${url}`, '--login=admin',
    `--password-file=${cliDir}/password`, '--ca-file=/run/almanac-secrets/tls.crt');
  stage = 'verify API and CLI observation parity';
  const apiSLI = await page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/slis/${id}`);
    return { status: response.status, body: await response.json(), etag: response.headers.get('etag') };
  }, evidence.sli_id);
  assert.equal(apiSLI.status, 200);
  const cliSLI = JSON.parse(await cli('slis', 'get', `--profile-file=${cliDir}/profile.json`, `--sli-id=${evidence.sli_id}`, '--output=json'));
  assert.equal(cliSLI.result.etag, apiSLI.etag);
  for (const key of ['id', 'name', 'display_name', 'datasource_id', 'measurement_revision', 'good_query', 'total_query']) {
    assert.equal(cliSLI.result.sli[key], apiSLI.body[key]);
  }
  const api = await page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/slis/${id}/observations?limit=50`);
    return { status: response.status, body: await response.json() };
  }, evidence.sli_id);
  assert.equal(api.status, 200);
  const cliResult = JSON.parse(await cli('slis', 'observations', `--profile-file=${cliDir}/profile.json`, `--sli-id=${evidence.sli_id}`, '--output=json'));
  const expected = api.body.items.find((item) => item.outcome === 'observed' && item.good_count === '99' && item.total_count === '100');
  assert.ok(expected);
  assert.deepEqual(cliResult.result.items.find((item) => item.observation_id === expected.observation_id), expected);
  await compose(['exec', '-T', 'app', 'rm', '-rf', cliDir]);
  return { datasource: datasource.body, sliID: evidence.sli_id, observation: expected, oldAgent };

}
async function recordHandoffProgress(datasourceID, sliID, targetID, phase) {
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
  if (![datasourceID, sliID, targetID].every((id) => uuid.test(id))) throw new Error('Invalid diagnostic identifier');
  const sql = `SELECT jsonb_build_object(
    'captured_at', now(), 'datasource_id', '${datasourceID}', 'sli_id', '${sliID}', 'target_agent_id', '${targetID}',
    'datasource', (SELECT jsonb_build_object('agent_id', agent_id, 'version', version, 'polling_interval_seconds', polling_interval_seconds, 'settling_delay_seconds', settling_delay_seconds) FROM public.almanac_datasources WHERE id='${datasourceID}'),
    'runtime', (SELECT jsonb_build_object('agent_id', agent_id, 'generation', generation, 'desired_revision', desired_configuration_revision,
      'applied_revision', applied_configuration_revision, 'scheduler_health', scheduler_health, 'collection_progress', collection_progress,
      'configuration_failure_code', configuration_failure_code, 'configuration_failure_revision', configuration_failure_revision,
      'last_heartbeat_at', last_heartbeat_at, 'last_attempt_at', last_attempt_at, 'last_successful_query_at', last_successful_query_at,
      'next_expected_execution_at', next_expected_execution_at, 'updated_at', updated_at)
      FROM public.almanac_agent_runtime_status WHERE agent_id='${targetID}'),
    'current_grant', (SELECT jsonb_build_object('generation', g.generation, 'configuration_revision', g.configuration_revision,
      'valid_from', g.valid_from, 'valid_until', g.valid_until, 'renewal_sequence', g.renewal_sequence,
      'allowed_measurements', COALESCE((SELECT jsonb_agg(jsonb_build_object('sli_id', item->>'sli_id', 'measurement_revision', item->>'measurement_revision'))
        FROM (SELECT value AS item FROM jsonb_array_elements(g.allowed_measurements)
          WHERE value->>'sli_id' ~ '^[0-9a-f-]{36}$' AND value->>'measurement_revision' ~ '^[0-9]{1,19}$' LIMIT 256) bounded), '[]'::jsonb))
      FROM public.almanac_agent_ownership o JOIN public.almanac_agent_execution_grants g ON g.lease_id=o.current_lease_id
      WHERE o.agent_id='${targetID}' ORDER BY o.generation DESC LIMIT 1),
    'epochs', COALESCE((SELECT jsonb_agg(to_jsonb(e)) FROM (SELECT sli_id, measurement_revision, agent_id, configuration_revision, starts_at, ends_at
      FROM public.almanac_sli_ownership_epochs WHERE sli_id='${sliID}' ORDER BY starts_at DESC LIMIT 50) e), '[]'::jsonb),
    'coverage', COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT agent_id, measurement_revision, configuration_revision,
      interval_start, interval_end, outcome, error_code, recorded_at FROM public.almanac_sli_coverage
      WHERE sli_id='${sliID}' ORDER BY interval_start DESC LIMIT 50) c), '[]'::jsonb),
    'coverage_counts', COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT agent_id, outcome, count(*) AS count
      FROM public.almanac_sli_coverage WHERE sli_id='${sliID}' GROUP BY agent_id, outcome ORDER BY agent_id, outcome LIMIT 100) c), '[]'::jsonb),
    'attempt_counts', COALESCE((SELECT jsonb_agg(to_jsonb(a)) FROM (SELECT agent_id, outcome,
      CASE WHEN error_code IS NULL THEN NULL WHEN error_code IN ('authentication', 'incomplete', 'network', 'query', 'rate_limited', 'tls', 'timeout', 'unsupported', 'assignment_revoked', 'deadline') THEN error_code ELSE 'other' END AS error_code,
      count(*) AS count, max(recorded_at) AS last_recorded_at FROM public.almanac_sli_execution_attempts WHERE sli_id='${sliID}'
      GROUP BY agent_id, outcome, 3 ORDER BY agent_id, outcome, 3 LIMIT 100) a), '[]'::jsonb)
  )`;
  const path = join(artifacts, `handoff-progress-${phase}.json`);
  const previousFailure = lastFailure;
  try {
    const output = await compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac', '-Atc', sql]);
    writeFileSync(path, JSON.stringify(JSON.parse(output), null, 2), { mode: 0o600 });
  } catch {
    writeFileSync(path, JSON.stringify({ phase, error: 'Handoff diagnostics unavailable.' }), { mode: 0o600 });
  } finally { lastFailure = previousFailure; }
}
async function checkAgentRetirement(page, original, collection) {
  const agentURL = `${url}/agents?agent=${original.agent.id}`;
  stage = 'real Compose assigned-agent deletion is blocked';
  await page.goto(agentURL);
  const runtime = page.getByRole('region', { name: 'Agent runtime', exact: true });
  await expect(runtime).not.toContainText('No datasources are assigned to this agent.');
  const runtimeExplanation = await page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/agents/${id}`);
    if (!response.ok) return null;
    return (await response.json()).runtime?.collection_explanation ?? null;
  }, original.agent.id);
  assert.equal(runtimeExplanation, 'none');
  await page.getByRole('button', { name: 'Rotate credential', exact: true }).click();
  const rotation = page.getByRole('dialog', { name: 'Rotate credential', exact: true });
  await expect(rotation.getByLabel('Current password', { exact: true })).toHaveCount(0);
  await expect(rotation.getByLabel('Keep the previous credential valid for', { exact: true })).toHaveValue('600');
  await rotation.getByRole('button', { name: 'Close dialog', exact: true }).click();
  await page.getByRole('button', { name: 'Revoke credential', exact: true }).click();
  const revoke = page.getByRole('dialog', { name: 'Revoke credential', exact: true });
  await expect(revoke.getByLabel('Current password', { exact: true })).toHaveCount(0);
  await revoke.getByRole('button', { name: 'Close dialog', exact: true }).click();
  await page.getByRole('button', { name: 'Delete agent', exact: true }).click();
  let dialog = page.getByRole('dialog', { name: 'Delete agent', exact: true });
  const sourceLink = dialog.getByRole('link', { name: 'Compose metrics (compose-metrics)', exact: true });
  await expect(sourceLink).toHaveAttribute('href', `/datasources/${collection.datasource.id}`);
  await expect(dialog.getByRole('button', { name: 'Register replacement agent', exact: true })).toBeVisible();
  await expect(dialog.getByRole('checkbox')).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: 'Delete agent', exact: true })).toBeDisabled();
  for (const theme of ['light', 'dark']) for (const viewport of [{ name: 'desktop', width: 1440, height: 1000 }, { name: 'mobile', width: 360, height: 900 }]) {
    await page.setViewportSize(viewport);
    await page.locator('html').evaluate((element, dark) => element.classList.toggle('dark', dark), theme === 'dark');
    assert.ok((await dialog.boundingBox()).width <= viewport.width);
    await page.screenshot({ path: join(artifacts, `delete-blocked-${viewport.name}-${theme}.png`), animations: 'disabled' });
  }
  stage = 'real Compose blocked datasource link uses app navigation';
  await sourceLink.click();
  await expect(page).toHaveURL(`${url}/datasources/${collection.datasource.id}`);
  await expect(page.getByLabel('Assigned agent', { exact: true })).toHaveValue(original.agent.id);
  await page.goBack();
  await expect(page).toHaveURL(agentURL);
  await page.getByRole('button', { name: 'Delete agent', exact: true }).click();
  dialog = page.getByRole('dialog', { name: 'Delete agent', exact: true });
  await dialog.getByRole('button', { name: 'Register replacement agent', exact: true }).click();
  const register = page.getByRole('dialog', { name: 'Register agent', exact: true });
  await register.getByLabel('Display name', { exact: true }).fill('Replacement');
  await expect(register.getByLabel('New credential expires after', { exact: true })).toHaveValue('7776000');
  await register.getByLabel('New credential expires after', { exact: true }).selectOption('custom');
  await register.getByLabel('Custom amount', { exact: true }).fill('90');
  await register.getByLabel('Unit', { exact: true }).selectOption('86400');
  for (const theme of ['light', 'dark']) for (const viewport of [{ name: 'desktop', width: 1440, height: 1000 }, { name: 'mobile', width: 360, height: 900 }]) {
    await page.setViewportSize(viewport);
    await page.locator('html').evaluate((element, dark) => element.classList.toggle('dark', dark), theme === 'dark');
    const amount = await register.getByLabel('Custom amount', { exact: true }).boundingBox();
    const unit = await register.getByLabel('Unit', { exact: true }).boundingBox();
    assert.ok(amount && unit && amount.y + amount.height <= unit.y && unit.x + unit.width <= viewport.width, 'custom duration fields overlap or overflow');
    await page.screenshot({ path: join(artifacts, `duration-custom-${viewport.name}-${theme}.png`), animations: 'disabled' });
  }
  await expect(register.getByLabel('Current password', { exact: true })).toHaveCount(0);
  stage = 'real Compose register and connect replacement';
  const created = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/agents');
  await register.getByRole('button', { name: 'Register agent', exact: true }).click();
  const createdResponse = await created;
  assert.equal(createdResponse.status(), 201);
  const replacement = await createdResponse.json(); replacementToken = replacement.token;
  assert.equal(createdResponse.request().postDataJSON().lifetime_seconds, 7776000);
  assert.equal((await page.locator('output[aria-label="Agent token"]').textContent()), replacementToken);
  await page.getByRole('button', { name: 'Close token', exact: true }).click();
  await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
  const replacementAgent = await startAgent(page, replacement.agent.id, replacementToken);
  stage = 'real Compose reassign actual datasource';
  await page.goto(`${url}/datasources/${collection.datasource.id}`);
  await expect.poll(async () => page.getByLabel('Assigned agent', { exact: true }).locator(`option[value="${replacement.agent.id}"]`).isEnabled(), { timeout: 25_000 }).toBe(true);
  await page.getByLabel('Assigned agent', { exact: true }).selectOption(replacement.agent.id);
  const saved = page.waitForResponse((response) => response.request().method() === 'PUT' && new URL(response.url()).pathname === `/api/v1/datasources/${collection.datasource.id}`);
  await page.getByRole('button', { name: 'Save settings', exact: true }).click();
  const savedResponse = await saved;
  assert.equal(savedResponse.status(), 200, `datasource reassignment returned HTTP ${savedResponse.status()}`);
  const moved = await savedResponse.json();
  assert.equal(moved.agent_id, original.agent.id);
  assert.equal(moved.reassignment?.target_agent_id, replacement.agent.id);
  await expect(page.getByRole('heading', { name: 'Moving datasource', exact: true })).toBeVisible();
  await expect.poll(async () => page.evaluate(async ({ id, target }) => {
    const r = await fetch(`/api/v1/datasources/${id}`); if (!r.ok) return false; const source = await r.json(); return source.agent_id === target && !source.reassignment;
  }, { id: collection.datasource.id, target: replacement.agent.id }), { timeout: 110_000, intervals: [1000] }).toBe(true);
  await page.getByRole('button', { name: 'Refresh reassignment', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Moving datasource', exact: true })).toHaveCount(0);
  stage = 'real Compose replacement collector resumes observations';
  await recordHandoffProgress(collection.datasource.id, collection.sliID, replacement.agent.id, 'start');
  try {
    await expect.poll(async () => (await compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac', '-Atc',
      `SELECT count(*) FROM public.almanac_sli_coverage WHERE sli_id='${collection.sliID}' AND agent_id='${replacement.agent.id}' AND outcome='observed'`])).trim(), { timeout: 110_000, intervals: [1000] }).not.toBe('0');
  } catch (error) {
    await recordHandoffProgress(collection.datasource.id, collection.sliID, replacement.agent.id, 'failure');
    throw error;
  }
  await recordHandoffProgress(collection.datasource.id, collection.sliID, replacement.agent.id, 'success');
  await stopAgent(collection.oldAgent);
  stage = 'real Compose delete reassigned agent and preserve history';
  await page.goto(agentURL);
  await page.getByRole('button', { name: 'Delete agent', exact: true }).click();
  dialog = page.getByRole('dialog', { name: 'Delete agent', exact: true });
  await expect(dialog.getByText('No datasources use this agent.', { exact: true })).toBeVisible();
  await expect(dialog.getByLabel('Current password', { exact: true })).toHaveCount(0);
  await dialog.getByRole('checkbox').check();
  const deleted = page.waitForResponse((response) => response.request().method() === 'DELETE' && new URL(response.url()).pathname === `/api/v1/agents/${original.agent.id}`);
  await dialog.getByRole('button', { name: 'Delete agent', exact: true }).click();
  const deletedResponse = await deleted;
  assert.equal(deletedResponse.status(), 200); assert.deepEqual(deletedResponse.request().postDataJSON(), {});
  await expect(page.getByRole('status').filter({ hasText: 'Agent deleted' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'local', exact: true })).toHaveCount(0);
  assert.equal(await page.evaluate(async (id) => (await fetch(`/api/v1/agents/${id}`)).status, original.agent.id), 404);
  assert.equal(await page.evaluate(async (token) => (await fetch('/api/agent/v1/configuration', { credentials: 'omit', headers: { Authorization: `Bearer ${token}` } })).status, issuedToken), 401);
  const observations = await page.evaluate(async (id) => (await (await fetch(`/api/v1/slis/${id}/observations?limit=50`)).json()).items, collection.sliID);
  assert.deepEqual(observations.find((item) => item.observation_id === collection.observation.observation_id), collection.observation);
  await checkDefaultCadenceSLI(page, replacement.agent.id);
  await stopAgent(replacementAgent);
}
async function checkDefaultCadenceSLI(page, agentID) {
  stage = 'real Compose default cadence datasource';
  await setCaptureViewport(page, 'light', { name: 'desktop', width: 1440, height: 1000 });
  await page.goto(`${url}/datasources/new`);
  await page.getByLabel('Display name', { exact: true }).fill('VictoriaMetrics');
  await page.getByLabel('Prometheus endpoint', { exact: true }).fill('http://victoriametrics:8428');
  await expect.poll(async () => page.getByLabel('Assigned agent', { exact: true }).locator(`option[value="${agentID}"]`).isEnabled(), { timeout: 25_000 }).toBe(true);
  await page.getByLabel('Assigned agent', { exact: true }).selectOption(agentID);
  await expect(page.getByLabel('Polling interval (seconds)', { exact: true })).toHaveValue('60');
  const datasourceResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/datasources');
  await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
  const sourceResponse = await datasourceResponse;
  assert.equal(sourceResponse.status(), 201);
  const source = await sourceResponse.json();
  assert.equal(source.polling_interval_seconds, 60);
  assert.equal(source.settling_delay_seconds, 60);
  stage = 'real Compose default cadence SLI creation';
  await page.goto(`${url}/slis/new`);
  await page.getByLabel('Display name', { exact: true }).fill('Demo request success');
  await page.getByLabel('Datasource', { exact: true }).selectOption(source.id);
  await page.getByLabel('Good count query', { exact: true }).fill('vector(99)');
  await page.getByLabel('Total count query', { exact: true }).fill('vector(100)');
  const creationStarted = performance.now();
  const createdResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/slis');
  await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
  const created = await createdResponse;
  assert.equal(created.status(), 201);
  const sli = await created.json();
  assert.equal(sli.datasource_id, source.id);
  const effective = Date.parse(sli.effective_from);
  const createdAt = Date.parse(sli.created_at);
  assert.ok(Number.isFinite(effective) && Number.isFinite(createdAt) && effective < createdAt);
  await expect(page.getByRole('heading', { name: 'Demo request success', exact: true })).toBeVisible();
  if (await page.locator('.sli-counts strong').count() === 0) await expect(page.getByText(/most recent complete interval/)).toBeVisible();
  await expect(page.getByRole('link', { name: 'VictoriaMetrics', exact: true })).toHaveAttribute('href', `/datasources/${source.id}`);
  await expect(page.locator(`a[href="/agents?agent=${agentID}"]`)).toBeVisible();
  await page.screenshot({ path: join(artifacts, 'sli-default-scheduled-light-desktop.png'), fullPage: true, animations: 'disabled' });
  writeFileSync(join(artifacts, 'sli-default-progress.json'), JSON.stringify({ stage: 'waiting', effective_from: sli.effective_from, created_at: sli.created_at, interval_seconds: 60, settling_seconds: 60 }) + '\n', { mode: 0o600 });
  stage = 'real Compose default cadence first observation';
  try {
    await expect.poll(async () => {
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      const counts = page.locator('.sli-counts strong');
      return await counts.count() === 2 ? [await counts.nth(0).textContent(), await counts.nth(1).textContent()] : [];
    }, { timeout: Math.max(1, 60_000 - (performance.now() - creationStarted)), intervals: [1000] }).toEqual(['99', '100']);
  } catch (error) {
    writeFileSync(join(artifacts, 'sli-default-progress.json'), JSON.stringify({ stage: 'first_observation_failed', effective_from: sli.effective_from, created_at: sli.created_at, interval_seconds: 60, settling_seconds: 60 }) + '\n', { mode: 0o600 });
    throw error;
  }
  const elapsedToObservation = performance.now() - creationStarted;
  assert.ok(elapsedToObservation < 60_000, 'first observation exceeded the 60-second cadence');
  const rows = await page.evaluate(async (id) => (await (await fetch(`/api/v1/slis/${id}/observations?limit=50`)).json()).items, sli.id);
  const first = rows.find((row) => row.outcome === 'observed' && row.good_count === '99' && row.total_count === '100');
  assert.ok(first);
  assert.equal(Date.parse(first.interval_end) - Date.parse(first.interval_start), 60_000);
  assert.ok(Date.parse(first.interval_start) >= effective);
  assert.ok(Date.parse(first.interval_end) <= createdAt, 'first measured interval must precede SLI creation');
  assert.ok(Date.parse(first.recorded_at) >= Date.parse(first.interval_end) + 60_000, 'first query must preserve source settling delay');
  for (const theme of ['light', 'dark']) for (const viewport of [{ name: 'desktop', width: 1440, height: 1000 }, { name: 'mobile', width: 360, height: 900 }]) {
    await setCaptureViewport(page, theme, viewport);
    await page.screenshot({ path: join(artifacts, `sli-default-observed-${viewport.name}-${theme}.png`), fullPage: true, animations: 'disabled' });
  }
  writeFileSync(join(artifacts, 'sli-default-progress.json'), JSON.stringify({ stage: 'observed', effective_from: sli.effective_from, created_at: sli.created_at, interval_start: first.interval_start, interval_end: first.interval_end, recorded_at: first.recorded_at, elapsed_to_observation_ms: Math.round(elapsedToObservation), interval_seconds: 60, settling_seconds: 60, good_count: '99', total_count: '100' }) + '\n', { mode: 0o600 });
}
async function checkLocalAgentRegistration() {
  const browser = await chromium.launch({ args: [`--ignore-certificate-errors-spki-list=${spki}`] });
  if (cancellation) { await browser.close(); ensureActive(); }
  activeBrowser = browser;
  let journeyPage;
  let documentMarker;
  const requests = [];
  let agentDetailReadSeen = false;
  let agentDetailReadSucceeded = false;
  let credentialHistoryReadSeen = false;
  let credentialHistoryReadSucceeded = false;
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
    page.on('response', (response) => {
      const pathname = new URL(response.url()).pathname;
      if (/^\/api\/v1\/agents\/[0-9a-f-]+$/.test(pathname)) {
        agentDetailReadSeen = true;
        agentDetailReadSucceeded = response.status() >= 200 && response.status() < 300;
      }
      if (/^\/api\/v1\/agents\/[0-9a-f-]+\/credentials$/.test(pathname)) {
        credentialHistoryReadSeen = true;
        credentialHistoryReadSucceeded = response.status() >= 200 && response.status() < 300;
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
    await page.evaluate(() => {
      window.composeVisibleLoading = false;
      new MutationObserver(() => {
        for (const el of document.querySelectorAll('.ui-loading')) {
          if (!el.closest('.sr-only') && el.getBoundingClientRect().width > 1) window.composeVisibleLoading = true;
        }
      }).observe(document.body, { childList: true, subtree: true });
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
        await navigateWorkspace(page, viewportName, 'Datasources');
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
        await navigateWorkspace(page, viewportName, 'Agents');
        await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
        await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeFocused();
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
    assert.equal(await page.evaluate(() => window.composeVisibleLoading), false, 'workspace navigation must not show loading indicators');
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
    const tokenValid = typeof result.token === 'string'
      && /^almanac_agent_v1_[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$/.test(result.token);
    assert.equal(tokenValid, true, 'registration did not return a valid one-time token');
    await expect(page.getByRole('dialog', { name: 'Agent token', exact: true })).toBeVisible();
    const displayedToken = await page.locator('output[aria-label="Agent token"]').textContent();
    assert.equal(typeof result.token === 'string' && result.token.length > 0 && displayedToken === result.token,
      true, 'the one-time token was not displayed');
    assert.deepEqual(requests, [{ name: 'local', display_name: 'Local', lifetime_seconds: 7776000 }]);
    stage = 'real Compose close one-time token dialog';
    await page.getByRole('button', { name: 'Close token', exact: true }).click();
    stage = 'real Compose verify agent detail URL after token close';
    await expect(page).toHaveURL((current) => current.pathname === '/agents' && current.searchParams.has('agent'));
    stage = 'real Compose verify credential history after token close';
    await expect(page.getByRole('heading', { name: 'Credential history', exact: true })).toBeVisible();
    await expect(page.getByText(result.credential.id, { exact: true })).toBeVisible();
    stage = 'real Compose verify Local display name after token close';
    await expect(page.locator('dd').getByText('Local', { exact: true })).toBeVisible();
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    const agentURL = page.url();
    stage = 'real Compose registration uses the existing administrator session';
    const registrationAuthContextRequests = contextRequests - contextRequestsBeforeRegistration;
    assert.equal(registrationAuthContextRequests, 0);
    for (const theme of ['light', 'dark']) {
      for (const viewport of viewports) {
        const viewportName = await setCaptureViewport(page, theme, viewport);
        stage = `real Compose ${theme} ${viewportName} agent detail screenshot after token closure`;
        await expect(page.getByRole('heading', { name: 'Credential history', exact: true })).toBeVisible();
        await expect(page.getByText(result.credential.id, { exact: true })).toBeVisible();
        const credentialRecord = page.getByRole('region', { name: `Credential ${result.credential.id}` });
        await expect(credentialRecord.getByText(result.credential.issued_at.replace('T', ' '), { exact: true })).toBeVisible();
        const revoke = credentialRecord.getByRole('button', { name: 'Revoke credential', exact: true });
        await revoke.scrollIntoViewIfNeeded();
        await expect(revoke).toBeInViewport();
        const bounds = await revoke.boundingBox();
        assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= viewport.width, 'revoke action clipped horizontally');
        await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
        const detailCapture = `agent-detail-after-token-${viewportName}-${theme}.png`;
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: join(artifacts, detailCapture), fullPage: true, animations: 'disabled' });
        detailCaptures.push(detailCapture);
      }
    }
    stage = 'real Compose detail to list browser back';
    await page.goBack();
    await expect(page).toHaveURL((current) => current.pathname === '/agents' && !current.searchParams.has('agent'));
    await expect(page.getByRole('button', { name: 'local', exact: true })).toBeVisible();
    await setCaptureViewport(page, 'light', viewports[0]);
    stage = 'real Compose agent row text alignment';
    const row = page.getByRole('button', { name: 'local', exact: true }).locator('..').locator('..');
    const centers = await row.evaluate((element) => {
      const textCenter = (node) => { const range = document.createRange(); range.selectNodeContents(node); const rect = range.getBoundingClientRect(); return rect.y + rect.height / 2; };
      return [textCenter(element.querySelector('button')), textCenter(element.querySelectorAll('td')[1])];
    });
    assert.ok(Math.abs(centers[0] - centers[1]) < 1, 'agent name text should align with display name text');
    stage = 'real Compose agent table pointer focus';
    const region = page.locator('.agents-table-scroll').and(page.getByRole('region', { name: 'Agent records', exact: true }));
    await expect(region).toHaveCount(1);
    await region.click({ position: { x: 2, y: 2 } });
    assert.equal(await region.evaluate((el) => el.matches(':focus-visible')), false, 'pointer focus should not draw keyboard outline');
    stage = 'real Compose agent table keyboard focus';
    await region.focus();
    await page.keyboard.press('Shift+Tab');
    await page.keyboard.press('Tab');
    await expect(region).toBeFocused();
    assert.equal(await region.evaluate((el) => el.matches(':focus-visible')), true, 'keyboard focus must remain visible');
    await page.screenshot({ path: join(artifacts, 'agent-list-keyboard-focus.png'), fullPage: true, animations: 'disabled' });
    stage = 'real Compose slow refresh and failure recovery';
    const agentListRoute = '**/api/v1/agents?*';
    let releaseSlowRead;
    const slowRead = new Promise((resolve) => { releaseSlowRead = resolve; });
    await withTrackedRoute(page, agentListRoute, async (route) => {
      await slowRead;
      await route.continue();
    }, async () => {
      await page.getByRole('button', { name: 'Refresh agents', exact: true }).click();
      await expect(page.getByRole('button', { name: 'local', exact: true })).toBeVisible();
      await expect(page.getByText('Refreshing agent records', { exact: true })).toHaveClass(/sr-only/);
      releaseSlowRead();
      await expect(page.getByText('Refreshing agent records', { exact: true })).toHaveCount(0);
    }, releaseSlowRead);
    stage = 'real Compose agent refresh failure and recovery';
    await withTrackedRoute(page, agentListRoute,
      (route) => route.fulfill({ status: 503, contentType: 'application/json', body: '{}' }),
      async () => {
        await page.getByRole('button', { name: 'Refresh agents', exact: true }).click();
        await expect(page.getByRole('alert')).toContainText('Showing stale results');
        await expect(page.getByRole('button', { name: 'local', exact: true })).toBeVisible();
      });
    await page.getByRole('button', { name: 'Refresh agents', exact: true }).click();
    await expect(page.getByRole('alert')).toHaveCount(0);
    stage = 'real Compose list to detail browser forward';
    await page.goForward();
    await expect(page).toHaveURL(agentURL);
    await expect(page.getByRole('heading', { name: 'Credential history', exact: true })).toBeVisible();
    await expect(page.getByText(result.credential.id, { exact: true })).toBeVisible();
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
    await expect(page.locator('dd').getByText('Local', { exact: true })).toBeVisible();
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
    const collection = await checkCollectedSLI(page, result.agent.id);
    await checkAgentRetirement(page, result, collection);
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
        failure_category: failureCategory(error),
        pathname,
        post_request_created: requests.length > 0,
        document_marker_matches: documentMarker
          ? await journeyPage.locator('html').getAttribute('data-compose-document-marker') === documentMarker
          : false,
        registration_dialog_visible: await visible(journeyPage.getByRole('dialog', { name: 'Register agent', exact: true })),
        token_dialog_visible: await visible(journeyPage.getByRole('dialog', { name: 'Agent token', exact: true })),
        token_output_present: await journeyPage.locator('output[aria-label="Agent token"]').count() > 0,
        agent_detail_read_seen: agentDetailReadSeen,
        agent_detail_read_succeeded: agentDetailReadSucceeded,
        credential_history_read_seen: credentialHistoryReadSeen,
        credential_history_read_succeeded: credentialHistoryReadSucceeded,
        credential_history_visible: await visible(journeyPage.getByRole('heading', { name: 'Credential history', exact: true })),
        local_display_name_visible: await visible(journeyPage.locator('dd').getByText('Local', { exact: true })),
      };
      writeFileSync(join(artifacts, 'local-agent-post-create-diagnostic.json'), JSON.stringify(safeState, null, 2), { mode: 0o600 });
      const noPasswords = await journeyPage.locator('input[type="password"]').evaluateAll((inputs) => inputs.every((input) => input.value === ''));
      if (!safeState.token_output_present && noPasswords) {
        await journeyPage.screenshot({ path: join(artifacts, 'local-agent-failed-screen.png'), fullPage: true, animations: 'disabled' });
      }
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
    for (const handle of launchers) {
      if (handle.child && !handle.exited) abortChild(handle.child, 'SIGTERM');
      if (handle.done) {
        const stopped = await Promise.race([handle.done.then(() => true), new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(false), 5000))]);
        if (!stopped) { cleanupFailed = true; console.error('Could not stop the acceptance launcher.'); }
      }
      if (handle.tokenFile) rmSync(handle.tokenFile, { force: true });
    }
    if (activeChild) {
      const child = activeChild; const done = activeChildDone;
      abortChild(child, 'SIGTERM');
      const stopped = await Promise.race([done.then(() => true), new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(false), 5000))]);
      if (!stopped) { cleanupFailed = true; console.error('Could not stop an active acceptance command.'); }
    }
    if (activeBrowser) {
      const browser = activeBrowser;
      activeBrowser = undefined;
      try { await browser.close(); } catch { cleanupFailed = true; console.error('Could not close acceptance browser.'); }
    }
    for (const handle of launchers) if (handle.container) {
      try {
        const ids = (await command('docker', ['ps', '-aq', '--filter', `id=${handle.container}`], { allowDuringCancellation: true })).trim();
        if (ids) {
          const info = JSON.parse(await command('docker', ['inspect', handle.container], { allowDuringCancellation: true }))[0];
          if (info.Config.Labels?.['almanac.local-agent'] !== 'owned' || info.Config.Labels?.['almanac.local-app'] !== handle.appID || info.HostConfig.NetworkMode !== `container:${handle.appID}`) throw new Error('launcher ownership mismatch');
          await command('docker', ['rm', '-f', handle.container], { allowDuringCancellation: true });
        }
      } catch { cleanupFailed = true; console.error('Could not remove the owned acceptance agent container.'); }
    }
    for (const name of [relayContainer, inputContainer]) {
      try {
        const ids = (await command('docker', ['ps', '-aq', '--filter', `name=^/${name}$`], { allowDuringCancellation: true })).trim();
        if (ids) await command('docker', ['rm', '-f', name], { allowDuringCancellation: true });
      } catch {
        cleanupFailed = true;
        console.error(`Could not remove owned acceptance container ${name}.`);
      }
    }
    try {
      const volumes = (await command('docker', ['volume', 'ls', '-q', '--filter', `name=^${inputVolume}$`], { allowDuringCancellation: true })).trim().split('\n');
      if (volumes.includes(inputVolume)) await command('docker', ['volume', 'rm', inputVolume], { allowDuringCancellation: true });
    } catch {
      cleanupFailed = true;
      console.error(`Could not remove owned acceptance input volume ${inputVolume}.`);
    }
    try {
      await command('docker', ['compose', '-f', 'compose.yaml', '-p', project,
        'down', '--volumes', '--remove-orphans', '--timeout', '35'], { timeout: 60_000, environment: env, allowDuringCancellation: true });
      if (await imageExists()) await command('docker', ['image', 'rm', `${project}:dev`], { timeout: 20_000, allowDuringCancellation: true });
      writeFileSync(join(artifacts, 'cleanup.json'), JSON.stringify({ containersVolumesNetworks: cleanupFailed ? 'failed or incomplete' : 'removed', image: 'removed if present' }), { mode: 0o600 });
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
  const readinessTemp = join(artifacts, 'readiness-temp');
  mkdirSync(readinessTemp, { mode: 0o700 });
  const readinessEnv = { ...env, TMPDIR: readinessTemp };
  const occupied = createServer((socket) => {
    socket.on('error', (error) => { if (error.code !== 'ECONNRESET') throw error; });
    socket.end('almanac-owned-port-fixture\n');
  });
  await new Promise((resolveListen, reject) => { occupied.once('error', reject); occupied.listen(18086, '127.0.0.1', resolveListen); });
  try {
    assert.equal(await probeOccupiedPort(), true, 'owned host listener was not reachable before startup');
    let runOutput;
    try { runOutput = await command('make', ['run'], { cwd: buildContext, timeout: 600_000, environment: readinessEnv }); }
    catch (error) { if (error.code === 'CANCELLED') throw error; }
    if (runOutput !== undefined) {
      writeFileSync(join(artifacts, 'occupied-port-unexpected-success.json'), JSON.stringify({
        listener_before_start: true, listener_after_start: await probeOccupiedPort(), stdout: redact(runOutput),
      }, null, 2), { mode: 0o600 });
      assert.fail('make run succeeded while the host port was occupied');
    }
    assert.ok(lastFailure, 'startup failed without command diagnostics');
    assert.match(`${lastFailure.stdout}\n${lastFailure.stderr}`, /(?:address already in use|port is already allocated|failed to bind host port|local host readiness failed)/i);
    assert.ok(!lastFailure.stdout.includes('Almanac is ready'));
    assert.equal(await probeOccupiedPort(), true, 'owned host listener was not reachable after failed startup');
    assert.deepEqual(readdirSync(readinessTemp).filter((name) => name.startsWith('almanac-readiness.')), []);
    writeFileSync(join(artifacts, 'occupied-port.json'), JSON.stringify(lastFailure), { mode: 0o600 });
    lastFailure = undefined;
  } finally { await new Promise((resolveClose) => occupied.close(resolveClose)); }
  stage = 'daily start after port released';
  await checkMake('run', readinessEnv);
  assert.deepEqual(readdirSync(readinessTemp).filter((name) => name.startsWith('almanac-readiness.')), []);
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
  const agentLogs = launcherLogs;
  assert.equal(!logs.includes(initial), true, 'Compose logs exposed the initial password');
  assert.equal(!logs.includes(permanent), true, 'Compose logs exposed the permanent password');
  assert.equal(typeof issuedToken === 'string' && issuedToken.length > 0, true,
    'registration did not issue a one-time token');
  assert.equal(!logs.includes(issuedToken), true, 'Compose logs exposed the one-time agent token');
  assert.equal(!agentLogs.includes(issuedToken), true, 'Agent logs exposed the one-time agent token');
  assert.equal(typeof replacementToken === 'string' && replacementToken.length > 0, true, 'replacement token was not issued');
  assert.equal(!logs.includes(replacementToken) && !agentLogs.includes(replacementToken), true, 'replacement token appeared in logs');
  const dbPassword = await compose(['exec', '-T', 'postgres', 'cat', '/run/postgres-secret/password']);
  assert.ok(dbPassword.length >= 32 && !logs.includes(dbPassword));
  writeFileSync(join(artifacts, 'acceptance.json'), JSON.stringify({ project,
    revision: (await command('git', ['rev-parse', 'HEAD'])).trim(), completed: true,
    harnessSHA256: Object.fromEntries(['check-local-compose.mjs', 'check-local-compose-cancellation.mjs']
      .map((name) => [name, createHash('sha256').update(readFileSync(join(root, 'web/scripts', name))).digest('hex')])),
    candidateDiffSHA256: createHash('sha256').update(await command('git', ['diff', 'HEAD', '--',
      'Dockerfile', '.dockerignore', 'compose.yaml', 'Makefile', 'deploy/local', 'scripts/check-local-readiness.sh', 'web/scripts/check-local-compose.mjs'])).digest('hex'),
    checks: ['initializer boundaries', 'real browser onboarding', 'stop/run persistence', 'repeat bootstrap persistence',
      'real browser agent registration with visible Local identifier suggestion and one-time token',
      'real agent collection, persisted 15-second 99/100 interval, browser and API/CLI parity',
      'nonroot/read-only/loopback', 'secret-free logs', 'invalid port refusal', 'occupied port refusal'],
  }, null, 2), { mode: 0o600 });
} catch (error) {
  failed = true;
  if (cancellation) stage = `cancelled by ${cancellation}`;
  const diagnostics = { stage, failure: { name: redact(error?.name), category: failureCategory(error), message: redact(error?.message) }, launcherOutput: redact(launcherOutput), lastFailure };
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
