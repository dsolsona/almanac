import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { resolve, join } from 'node:path';

const root = resolve(import.meta.dirname, '../..');
const harness = join(root, 'web/scripts/check-local-compose.mjs');
const maxOutput = 256 * 1024;
const timeoutMs = 600_000;
let failureArtifacts;
let cancellation;
let activeFixture;

process.on('SIGINT', () => forwardSignal('SIGINT'));
process.on('SIGTERM', () => forwardSignal('SIGTERM'));
function forwardSignal(signal) {
  if (cancellation) return;
  cancellation = signal;
  if (activeFixture && activeFixture.exitCode === null && activeFixture.signalCode === null) activeFixture.kill(signal);
}

function bounded(promise, fallback, milliseconds) {
  let timer;
  return Promise.race([promise, new Promise((resolveWait) => {
    timer = setTimeout(() => resolveWait(fallback), milliseconds);
  })]).finally(() => clearTimeout(timer));
}

function run(executable, args, timeout = 20_000) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(executable, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
    const collect = (target, chunk) => {
      if (Buffer.byteLength(target.value) + chunk.length > maxOutput) {
        child.kill('SIGKILL');
        rejectRun(new Error('bounded output exceeded'));
      } else target.value += chunk.toString('utf8');
    };
    const out = { value: '' };
    const err = { value: '' };
    child.stdout.on('data', (chunk) => collect(out, chunk));
    child.stderr.on('data', (chunk) => collect(err, chunk));
    const timer = setTimeout(() => { child.kill('SIGKILL'); rejectRun(new Error('bounded wait expired')); }, timeout);
    child.once('error', () => { clearTimeout(timer); rejectRun(new Error('child could not start')); });
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      if (code !== 0) rejectRun(Object.assign(new Error('inspection command failed'), { status: code }));
      else resolveRun(out.value);
    });
  });
}

async function assertNoResources(project) {
  assert.match(project, /^almanac-check-\d+-[a-f0-9]{8}$/);
  assert.notEqual(project, 'almanac-local');
  for (const kind of ['container', 'network', 'volume']) {
    const flags = kind === 'container' ? '-aq' : '-q';
    const result = await run('docker', [kind, 'ls', flags, '--filter', `label=com.docker.compose.project=${project}`]);
    assert.equal(result.trim(), '');
  }
  try {
    await run('docker', ['image', 'inspect', `${project}:dev`]);
    assert.fail('generated image tag remains');
  } catch (error) {
    if (error.message === 'generated image tag remains' || error.status !== 1) throw error;
  }
}

async function exercise(signal, expectedCode) {
  failureArtifacts = undefined;
  const child = spawn(process.execPath, [harness, '--cancellation-fixture'], { cwd: root,
    stdio: ['ignore', 'pipe', 'pipe'] });
  activeFixture = child;
  let stdout = '';
  let stdoutBytes = 0;
  let stderrBytes = 0;
  let ready;
  let owned;
  let resolveReady;
  const readyPromise = new Promise((resolveReadyPromise) => { resolveReady = resolveReadyPromise; });
  let exited;
  const exitPromise = new Promise((resolveExit) => { exited = resolveExit; });
  const timer = setTimeout(() => {
    resolveReady(undefined);
    child.kill('SIGTERM');
  }, timeoutMs);
  child.stdout.on('data', (chunk) => {
    stdoutBytes += chunk.length;
    if (stdoutBytes > maxOutput) { resolveReady(undefined); child.kill('SIGTERM'); return; }
    stdout += chunk.toString('utf8');
    while (stdout.includes('\n')) {
      const line = stdout.slice(0, stdout.indexOf('\n'));
      stdout = stdout.slice(stdout.indexOf('\n') + 1);
      try {
        const event = JSON.parse(line);
        assert.match(event.project, /^almanac-check-\d+-[a-f0-9]{8}$/);
        assert.ok(event.artifacts.startsWith(join(root, '.local/compose-check-')));
        if (event.owned) { owned = event; failureArtifacts = event.artifacts; }
        if (event.ready) { ready = event; resolveReady(ready); }
      } catch { resolveReady(undefined); }
    }
  });
  child.stderr.on('data', (chunk) => {
    stderrBytes += chunk.length;
    if (stderrBytes > maxOutput) { resolveReady(undefined); child.kill('SIGTERM'); }
  });
  child.once('error', () => { clearTimeout(timer); resolveReady(undefined); exited({ code: -1, signal: 'ERROR' }); });
  child.once('close', (code, childSignal) => {
    clearTimeout(timer);
    if (!ready) resolveReady(undefined);
    exited({ code, signal: childSignal });
  });
  try {
    const report = await bounded(readyPromise, undefined, timeoutMs);
    assert.ok(report?.ready === true);
    assert.match(report.project, /^almanac-check-\d+-[a-f0-9]{8}$/);
    assert.ok(report.artifacts.startsWith(join(root, '.local/compose-check-')));
    failureArtifacts = report.artifacts;
    child.kill(signal);
    const result = await bounded(exitPromise, { code: -1 }, 110_000);
    if (cancellation) assert.ok(result.code === 130 || result.code === 143);
    else assert.equal(result.code, expectedCode);
    await assertNoResources(report.project);
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      const closed = await bounded(exitPromise, undefined, 110_000);
      if (!closed) {
        child.kill('SIGKILL');
        await bounded(exitPromise, undefined, 5000);
        console.error(`Fixture could not finish cleanup; inspect owned project ${owned?.project ?? 'unknown'}; evidence: ${failureArtifacts ?? 'unavailable'}.`);
      }
    }
    if (activeFixture === child) activeFixture = undefined;
    clearTimeout(timer);
  }
}

try {
  await exercise('SIGINT', 130);
  if (!cancellation) await exercise('SIGTERM', 143);
  if (cancellation) process.exitCode = cancellation === 'SIGINT' ? 130 : 143;
  else console.log('PASS local Compose cancellation cleanup');
} catch {
  if (cancellation) process.exitCode = cancellation === 'SIGINT' ? 130 : 143;
  else {
    console.error(`FAIL local Compose cancellation regression; child output withheld; safe evidence: ${failureArtifacts ?? 'READY was not received'}.`);
    process.exitCode = 1;
  }
}
