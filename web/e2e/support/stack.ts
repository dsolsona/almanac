import { execFileSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';

// Access to the Compose stack under test (scripts/e2e.sh). Tests run from web/, inside the
// dev container with the Docker socket; the stack's ports come from the environment.
const project = process.env.COMPOSE_PROJECT_NAME ?? 'almanac-e2e';

export const metricsURL = process.env.E2E_METRICS_URL ?? 'http://localhost:18528';

function docker(args: string[], input?: string): string {
  return execFileSync('docker', args, { encoding: 'utf8', input, stdio: ['pipe', 'pipe', 'pipe'], timeout: 120_000 });
}

export function compose(...args: string[]): string {
  return docker(['compose', '-p', project, '-f', '../compose.yaml', '-f', '../compose.e2e.yaml', ...args]);
}

export function localCommand(target: 'agent-seed' | 'run' | 'start'): string {
  return execFileSync('make', [target], { cwd: '..', encoding: 'utf8', timeout: 120_000,
    env: { ...process.env, COMPOSE_FILE: 'compose.yaml:compose.e2e.yaml' },
    stdio: ['pipe', 'pipe', 'pipe'] });
}

export function initialPassword(): string {
  return compose('exec', '-T', 'app', 'cat', '/run/almanac-secrets/initial-admin-password').trim();
}

// Restarts the application. Local agents share the app container's network namespace (as `make agent`
// does), which Docker replaces on restart, so they are restarted after it to rejoin the new one.
// Runs SQL in the stack's PostgreSQL as its owner (local socket inside the container). Only the
// performance fixture uses this, to write retained history that would take weeks to collect.
export function psql(sql: string): string {
  return docker(['compose', '-p', project, '-f', '../compose.yaml', '-f', '../compose.e2e.yaml', 'exec', '-T', 'postgres', 'psql', '--no-psqlrc', '--quiet',
    '--tuples-only', '--no-align', '-v', 'ON_ERROR_STOP=1', '-U', 'almanac', '-d', 'almanac', '-f', '-'], sql).trim();
}

export function restartApp(): void {
  compose('restart', 'app');
  compose('up', '--detach', '--wait', '--wait-timeout', '90', 'app');
  const agents = docker(['ps', '--quiet', '--filter', 'label=almanac.e2e=owned']).split('\n').filter(Boolean);
  if (agents.length) docker(['restart', ...agents]);
}

function appContainer(): string {
  const id = compose('ps', '--quiet', 'app').trim();
  if (!id) throw new Error('the end-to-end app container is not running');
  return id;
}

// Starts the real agent image the way `make agent` does: on the app's network namespace, read-only,
// with no capabilities. The credential is passed through the environment because this stack is
// disposable test data; the operator launcher (scripts/local-agent.sh) keeps it off the command line.
export function startAgent(token: string): string {
  const app = appContainer();
  const ca = compose('exec', '-T', 'app', 'cat', '/run/almanac-secrets/tls.crt');
  const name = `almanac-e2e-agent-${randomBytes(6).toString('hex')}`;
  return docker(['run', '--detach', '--name', name, '--label', 'almanac.e2e=owned',
    '--network', `container:${app}`, '--user', '10001:10001', '--read-only', '--cap-drop', 'ALL',
    '--security-opt', 'no-new-privileges', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m,mode=1777',
    '--env', `AGENT_TOKEN=${token}`, '--env', `AGENT_CA=${ca}`, '--env', 'OTEL_SDK_DISABLED=true',
    '--entrypoint', '/bin/sh', `${project}-agent:dev`, '-c',
    'umask 077; printf %s "$AGENT_TOKEN" > /tmp/agent-token; printf %s "$AGENT_CA" > /tmp/agent-ca.pem; unset AGENT_TOKEN AGENT_CA; exec /usr/local/bin/almanac-agent run --backend-url=https://localhost:8080 --token-file=/tmp/agent-token --ca-file=/tmp/agent-ca.pem']).trim();
}

export function stopAgent(container: string): void {
  docker(['rm', '--force', container]);
}

export function agentLogs(container: string): string {
  return docker(['logs', container]);
}
