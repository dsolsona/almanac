import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

// Check orchestration boundaries; the real-stack journey covers SQL and agent authentication.
const root = resolve(import.meta.dirname, '../..');
const dir = mkdtempSync(join(tmpdir(), 'almanac-dev-agent-check-'));
const log = join(dir, 'docker.log');
writeFileSync(join(dir, 'docker'), `#!/bin/sh
printf '%s\\n' "$*" >> "$FAKE_LOG"
case "$*" in
  *'if [ -f /run/almanac-agent/token ]'*) [ "$FAKE_SEEDED" != 1 ] || echo seeded ;;
  *'exec -T app cat'*) echo 'public certificate' ;;
  *'run --rm -T migrate'*) [ "$FAKE_MIGRATE_FAIL" != 1 ] ;;
  *'exec -T --user root postgres sh -s'*) cat >/dev/null ;;
esac
`, { mode: 0o700 });
function run(mode, extra = {}) {
  writeFileSync(log, '');
  const result = spawnSync('sh', ['scripts/local-dev-agent.sh', mode], { cwd: root, encoding: 'utf8',
    env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, FAKE_LOG: log, ...extra } });
  assert.ifError(result.error);
  return { ...result, calls: readFileSync(log, 'utf8') };
}
try {
  let result = run('start');
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.calls, /build agent|up --detach/);
  result = run('start', { FAKE_SEEDED: '1' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.calls, /--profile agent build agent/);
  assert.match(result.calls, /--profile agent up --detach --no-deps --force-recreate agent/);
  result = run('seed', { FAKE_MIGRATE_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.calls, /postgres sh -s/);
  result = run('seed');
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.calls, /exec -T --user root postgres sh -s/);
  console.log('Seeded local agent: 4 orchestration cases passed');
} finally {
  rmSync(dir, { recursive: true, force: true });
}
