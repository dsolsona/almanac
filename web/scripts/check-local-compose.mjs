import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash, randomBytes, X509Certificate } from 'node:crypto';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { createServer } from 'node:net';
import { chromium, expect } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');
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

function redact(value) {
  let text = String(value ?? '');
  for (const secret of [initial, permanent]) if (secret) text = text.split(secret).join('[REDACTED]');
  return text.replace(/postgres(?:ql)?:\/\/[^\s"']+/g, '[DATABASE URL]')
    .replace(/\b[a-fA-F0-9]{64}\b/g, '[HEX SECRET OR DIGEST]')
    .replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----/g, '[PRIVATE KEY]')
    .slice(-64 * 1024);
}

function command(executable, args, { input, timeout = 120_000, environment = env } = {}) {
  try {
    return execFileSync(executable, args, { cwd: root, env: environment, input, timeout,
      maxBuffer: 32 * 1024 * 1024, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'] });
  } catch (error) {
    lastFailure = { executable, args, status: error.status, signal: error.signal,
      code: error.code, stdout: redact(error.stdout), stderr: redact(error.stderr) };
    throw error;
  }
}
function compose(args) {
  return command('docker', ['compose', '-f', 'compose.yaml', '-p', project, ...args]);
}
function checkMake(target) {
  const output = command('make', [target], { timeout: 600_000 });
  assert.ok(!initial || !output.includes(initial));
  assert.ok(!permanent || !output.includes(permanent));
  writeFileSync(join(artifacts, `${target}-${Date.now()}.txt`), output, { mode: 0o600 });
}
function secret(name) {
  return compose(['exec', '-T', 'app', 'cat', `/run/almanac-secrets/${name}`]);
}
function databaseState() {
  return compose(['exec', '-T', 'postgres', 'psql', '-U', 'almanac', '-d', 'almanac',
    '-Atc', 'SELECT installation_id, bootstrap_admin_id, onboarding_completed_at IS NOT NULL FROM almanac_installation']);
}
function secretDigest() {
  return createHash('sha256').update(secret('database-url')).update(secret('initial-admin-password'))
    .update(secret('tls.crt')).update(secret('tls.key')).digest('hex');
}
async function checkPermanentLogin() {
  const browser = await chromium.launch({ args: [`--ignore-certificate-errors-spki-list=${spki}`] });
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
    await browser.close();
  }
}

try {
  // This unique project owns every resource that this harness will remove.
  checkMake('bootstrap');
  stage = 'focused initializer checks';
  const checks = compose(['run', '--rm', '--no-deps', '--entrypoint', 'sh', 'init',
    '/usr/local/lib/almanac/init-test.sh']);
  assert.match(checks, /PASS/);
  stage = 'inspect isolated runtime';
  const config = JSON.parse(compose(['config', '--format', 'json']));
  assert.equal(config.services.app.user, '10001:10001');
  assert.equal(config.services.app.read_only, true);
  assert.ok(!config.services.postgres.ports?.length);
  assert.equal(config.services.app.ports[0].host_ip, '127.0.0.1');
  assert.equal(String(config.services.app.ports[0].published), '18086');
  initial = secret('initial-admin-password');
  assert.ok(initial.length >= 32 && !initial.includes('\n'));
  certificate = secret('tls.crt');
  const publicKey = new X509Certificate(certificate).publicKey.export({ type: 'spki', format: 'der' });
  spki = createHash('sha256').update(publicKey).digest('base64');
  const beforeSecrets = secretDigest();
  const postgresID = compose(['ps', '-q', 'postgres']).trim();
  permanent = `Compose permanent ${randomBytes(20).toString('hex')}`;
  stage = 'real browser onboarding';
  const result = command(process.execPath, [join(root, 'web/scripts/check-auth-browser.mjs')], {
    input: JSON.stringify({ scenario: 'onboarding', url, login: 'admin', initial,
      permanent, certificate, spki, artifacts }), timeout: 60_000,
  });
  assert.match(result, /PASS browser-auth onboarding/);
  const completed = databaseState();
  assert.match(completed.trim(), /\|t$/);
  stage = 'invalid port fails before changing running stack';
  assert.throws(() => command('make', ['run'], { environment: { ...env, ALMANAC_PORT: 'invalid' } }));
  assert.match(lastFailure.stderr, /ALMANAC_PORT must/);
  writeFileSync(join(artifacts, 'invalid-port.json'), JSON.stringify(lastFailure), { mode: 0o600 });
  lastFailure = undefined;
  assert.equal(databaseState(), completed);
  stage = 'stop and daily start preserve onboarding';
  checkMake('stop');
  stage = 'occupied port fails without false readiness';
  const occupied = createServer();
  await new Promise((resolve, reject) => { occupied.once('error', reject); occupied.listen(18086, '127.0.0.1', resolve); });
  try {
    assert.throws(() => command('make', ['run']));
    assert.ok(!lastFailure.stdout.includes('Almanac is ready'));
    writeFileSync(join(artifacts, 'occupied-port.json'), JSON.stringify(lastFailure), { mode: 0o600 });
    lastFailure = undefined;
  } finally {
    await new Promise((resolve) => occupied.close(resolve));
  }
  stage = 'daily start after port released';
  checkMake('run');
  assert.equal(compose(['ps', '-q', 'postgres']).trim(), postgresID);
  assert.equal(secretDigest(), beforeSecrets);
  assert.equal(databaseState(), completed);
  await checkPermanentLogin();
  stage = 'repeat bootstrap preserves onboarding';
  checkMake('bootstrap');
  assert.equal(compose(['ps', '-q', 'postgres']).trim(), postgresID);
  assert.equal(secretDigest(), beforeSecrets);
  assert.equal(databaseState(), completed);
  await checkPermanentLogin();
  stage = 'logs exclude credentials';
  const logs = compose(['logs', '--no-color']);
  assert.ok(!logs.includes(initial));
  assert.ok(!logs.includes(permanent));
  const dbPassword = compose(['exec', '-T', 'postgres', 'cat', '/run/postgres-secret/password']);
  assert.ok(dbPassword.length >= 32 && !logs.includes(dbPassword));
  writeFileSync(join(artifacts, 'acceptance.json'), JSON.stringify({ project,
    revision: command('git', ['rev-parse', 'HEAD']).trim(), completed: true,
    candidateDiffSHA256: createHash('sha256').update(command('git', ['diff', 'HEAD', '--',
      'Dockerfile', '.dockerignore', 'compose.yaml', 'Makefile', 'deploy/local',
      'web/scripts/check-local-compose.mjs'])).digest('hex'),
    checks: ['initializer boundaries', 'real browser onboarding', 'stop/run persistence',
      'repeat bootstrap persistence', 'nonroot/read-only/loopback', 'secret-free logs',
      'invalid port refusal', 'occupied port refusal'],
  }, null, 2), { mode: 0o600 });
} catch {
  failed = true;
  const diagnostics = { stage, lastFailure };
  // Preserve first-attempt evidence before destroying the isolated project. Never
  // persist raw exceptions or browser call logs, which may contain form values.
  try { diagnostics.services = redact(compose(['ps', '-a', '--format', 'json'])); } catch { diagnostics.services = 'unavailable'; }
  try {
    const appID = compose(['ps', '-aq', 'app']).trim();
    if (/^[a-f0-9]+$/.test(appID)) diagnostics.appRuntime = redact(command('docker', ['inspect',
      '--format', '{"state":{{json .State}},"networks":{{json .NetworkSettings.Networks}},"networkMode":{{json .HostConfig.NetworkMode}}}', appID]));
  } catch { diagnostics.appRuntime = 'unavailable'; }
  try { diagnostics.logs = redact(compose(['logs', '--no-color', '--tail', '80'])); } catch { diagnostics.logs = 'unavailable'; }
  writeFileSync(join(artifacts, 'diagnostics.json'), JSON.stringify(diagnostics, null, 2), { mode: 0o600 });
  writeFileSync(join(artifacts, 'compose-failure.txt'), `Failed stage: ${stage}\n`, { mode: 0o600 });
  console.error(`FAIL local Compose at ${stage}; sanitized evidence: ${artifacts}`);
} finally {
  try {
    compose(['down', '--volumes', '--remove-orphans', '--timeout', '35']);
  } catch {
    cleanupFailed = true;
    console.error(`Could not clean owned acceptance project ${project}; inspect it before rerunning.`);
  }
}
if (failed || cleanupFailed) process.exitCode = 1;
else console.log(`PASS local Compose startup, onboarding, repeat bootstrap and restart; evidence: ${artifacts}`);
