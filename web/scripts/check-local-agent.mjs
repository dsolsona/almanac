import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

// Fake Docker only tests launcher boundaries. The Compose journey proves real operation.
const root = resolve(import.meta.dirname, '../..');
const dir = mkdtempSync(join(tmpdir(), 'almanac-launcher-check-'));
const log = join(dir, 'docker.log');
const token = join(dir, 'token');
const fake = join(dir, 'docker');
writeFileSync(token, 'fixture-credential', { mode: 0o600 });
writeFileSync(fake, `#!/bin/sh
printf '%s\\n' "$*" >> "$FAKE_LOG"
case "$1 $2" in
  'compose -f') case "$*" in *' ps '*) printf 'abc123\\n';; *' exec '*) printf 'public certificate\\n';; esac ;;
  'inspect --format') case "$3" in *Health*) printf 'healthy\\n';; *) printf 'sha256:abc\\n';; esac ;;
  'create -i') [ "$FAKE_CREATE_FAIL" != 1 ] || exit 1; printf 'owned-container-id\\n' ;;
  'start --attach') exec sleep 30 ;;
  'rm -f') : ;;
  *) exit 1 ;;
esac
`, { mode: 0o700 });
const env = { ...process.env, PATH: `${dir}:${process.env.PATH}`, TMPDIR: dir,
  AGENT_TOKEN_FILE: token, FAKE_LOG: log };
function run(extra = {}) {
  const child = spawn('make', ['agent'], { cwd: root, env: { ...env, ...extra }, detached: true });
  let output = '';
  child.stdout.on('data', (value) => { output += value; });
  child.stderr.on('data', (value) => { output += value; });
  const done = new Promise((resolveDone, reject) => {
    child.on('error', reject);
    child.on('close', (code, signal) => resolveDone({ code, signal, output }));
  });
  return { child, done };
}
try {
  chmodSync(token, 0o644);
  let result = await run().done;
  assert.notEqual(result.code, 0);
  assert.match(result.output, /owner-only/);
  chmodSync(token, 0o600);
  writeFileSync(log, '');
  result = await run({ FAKE_CREATE_FAIL: '1' }).done;
  assert.notEqual(result.code, 0);
  assert.doesNotMatch(readFileSync(log, 'utf8'), /^rm /m, 'failed create must not remove a container');
  writeFileSync(log, '');
  const running = run();
  for (let i = 0; i < 100; i++) {
    if (readFileSync(log, 'utf8').includes('start --attach')) break;
    await new Promise((resolveWait) => setTimeout(resolveWait, 50));
  }
  assert.match(readFileSync(log, 'utf8'), /start --attach --interactive owned-container-id/);
  process.kill(-running.child.pid, 'SIGINT');
  result = await running.done;
  const calls = readFileSync(log, 'utf8');
  assert.match(calls, /rm -f owned-container-id/);
  assert.doesNotMatch(calls + result.output, /fixture-credential/);
  assert.match(calls, /container:abc123/);
  assert.match(calls, /--read-only/);
  console.log('Local agent launcher: 3 boundary cases passed');
} finally {
  rmSync(dir, { recursive: true, force: true });
}
