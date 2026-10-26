import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, X509Certificate } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { createServer } from 'node:net';
import { chromium, expect } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');
const fixtureMode = process.argv.length === 3 && process.argv[2] === '--cancellation-fixture';
if (process.argv.length !== 2 && !fixtureMode) throw new Error('Unsupported arguments');
mkdirSync(join(root, '.local'), { recursive: true });
const artifacts = mkdtempSync(join(root, '.local/compose-check-'));
const project = `almanac-check-${process.pid}-${randomBytes(4).toString('hex')}`;
const env = { ...process.env, COMPOSE_PROJECT_NAME: project, ALMANAC_PORT: '18086' };
delete env.COMPOSE_FILE;
const url = 'https://localhost:18086';
let stage = 'build and bootstrap';
let failed = false;
let cleanupFailed = false;
let initial;
let permanent;
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
  for (const secret of [initial, permanent]) if (secret) text = text.split(secret).join('[REDACTED]');
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

function command(executable, args, { input, timeout = 120_000, environment = env, allowDuringCancellation = false } = {}) {
  if (!allowDuringCancellation) ensureActive();
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, args, { cwd: root, env: environment,
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
  const output = await command('make', [target], { timeout: 600_000 });
  assert.ok(!initial || !output.includes(initial));
  assert.ok(!permanent || !output.includes(permanent));
  writeFileSync(join(artifacts, `${target}-${Date.now()}.txt`), output, { mode: 0o600 });
}
async function secret(name) { return compose(['exec', '-T', 'app', 'cat', `/run/almanac-secrets/${name}`]); }
async function databaseState() {
  return compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac',
    '-Atc', 'SELECT installation_id, bootstrap_admin_id, onboarding_completed_at IS NOT NULL FROM almanac_installation']);
}
async function secretDigest() {
  return createHash('sha256').update(await secret('database-url')).update(await secret('initial-admin-password'))
    .update(await secret('tls.crt')).update(await secret('tls.key')).digest('hex');
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
    await expect(page.getByRole('heading', { name: 'Signed in', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
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
  if (fixtureMode) console.log(JSON.stringify({ owned: true, project, artifacts }));
  await checkMake('bootstrap');
  if (fixtureMode) {
    // Keep a real child command active when the regression sends its signal.
    const pending = command(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { timeout: 600_000 });
    console.log(JSON.stringify({ ready: true, project, artifacts }));
    await pending;
    throw Object.assign(new Error('cancelled'), { code: 'CANCELLED' });
  }
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
  await assert.rejects(command('make', ['run'], { environment: { ...env, ALMANAC_PORT: 'invalid' } }));
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
    await assert.rejects(command('make', ['run']));
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
} finally { await cleanup(); }

if (cancellation) process.exitCode = cancellation === 'SIGINT' ? 130 : 143;
else if (failed || cleanupFailed) process.exitCode = 1;
else console.log(`PASS local Compose startup, onboarding, repeat bootstrap and restart; evidence: ${artifacts}`);
