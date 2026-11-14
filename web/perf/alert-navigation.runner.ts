import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { navigationState, navigationFailures, runNavigationClient, type NavigationStart } from './alert-navigation.client.ts';
export { publicOutcome } from './alert-navigation.client.ts';

// Private JSONL stdin only. No CLI URLs, files, state paths, credentials or arbitrary options.
// A start line (<=1 MiB excluding LF) then a stop line (<=1 KiB), or EOF.
// Stop/EOF ends API cycles only after ten cycles/user AND the browser lane settles.
export const maxStartBytes = 1024 * 1024;
type Stop = {type: 'stop'; runID: string};
type Message = NavigationStart | Stop;
const object = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const keys = (v: Record<string, unknown>, expected: string[]) => Object.keys(v).every(k => expected.includes(k)) && expected.every(k => k in v);
const uuid = (v: unknown) => typeof v === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const text = (v: unknown, max: number): v is string => typeof v === 'string' && v.length <= max;

function validStart(value: unknown): value is NavigationStart {
  if (!object(value) || !keys(value, ['type', 'runID', 'baseURL', 'fleet', 'storageState']) || value.type !== 'start' ||
    typeof value.runID !== 'string' || !/^[a-zA-Z0-9_-]{1,64}$/.test(value.runID) || !text(value.baseURL, 2048)) return false;
  let url: URL;
  try { url = new URL(value.baseURL); } catch { return false; }
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) return false;
  const fleet = value.fleet;
  if (!object(fleet) || !keys(fleet, ['slis','slos'])) return false;
  for (const name of ['slis', 'slos']) {
    const entries = fleet[name];
    if (!Array.isArray(entries) || entries.length !== 200 || entries.some(e => !object(e) ||
      !keys(e, name === 'slis' ? ['id','good'] : ['id']) || !uuid(e.id) ||
      (name === 'slis' && (typeof e.good !== 'number' || !Number.isInteger(e.good) || e.good < 0 || e.good > 10000))) ||
      new Set(entries.map(e => e.id)).size !== 200) return false;
  }
  const state = value.storageState;
  if (!object(state) || !keys(state, ['cookies','origins']) || !Array.isArray(state.cookies) || state.cookies.length > 100 ||
    !Array.isArray(state.origins) || state.origins.length > 10) return false;
  // Only state for the selected origin is accepted. Do not silently transmit broad-domain credentials.
  if (!state.cookies.every(c => object(c) && keys(c, ['name','value','domain','path','expires','httpOnly','secure','sameSite']) &&
    text(c.name,256) && c.name.length > 0 && text(c.value,65536) &&
    (c.domain === url.hostname || c.domain === `.${url.hostname}`) && text(c.path,2048) && c.path.startsWith('/') &&
    typeof c.expires === 'number' && Number.isFinite(c.expires) && typeof c.httpOnly === 'boolean' &&
    typeof c.secure === 'boolean' && ['Strict','Lax','None'].includes(String(c.sameSite)))) return false;
  return state.origins.every(o => object(o) && keys(o, ['origin','localStorage']) && o.origin === url.origin &&
    Array.isArray(o.localStorage) && o.localStorage.length <= 100 && o.localStorage.every(s => object(s) &&
      keys(s,['name','value']) && text(s.name,256) && text(s.value,65536)));
}

export class NavigationProtocol {
  private pending = Buffer.alloc(maxStartBytes);
  private length = 0;
  private runID: string | undefined;
  private stopped = false;
  private ended = false;
  private emit: (message: Message) => void;
  constructor(emit: (message: Message) => void) { this.emit = emit; }
  push(chunk: Buffer) {
    if (this.ended) throw new Error('input_closed');
    // Process each bounded line before appending; never buffer an arbitrary stdin chunk.
    let offset = 0;
    while (offset < chunk.length) {
      const newline = chunk.indexOf(10, offset);
      const end = newline < 0 ? chunk.length : newline;
      const limit = this.runID === undefined ? maxStartBytes : 1024;
      if (this.length + end - offset > limit) throw new Error('input_too_large');
      chunk.copy(this.pending,this.length,offset,end);
      this.length += end-offset;
      if (newline < 0) return;
      this.line();
      offset = newline + 1;
    }
  }
  private line() {
    let value: unknown;
    try { value = JSON.parse(new TextDecoder('utf-8', {fatal:true}).decode(this.pending.subarray(0,this.length))); }
    catch { throw new Error('invalid_json'); }
    finally { this.pending = Buffer.alloc(1024); this.length=0; }
    if (this.runID === undefined) {
      if (!validStart(value)) throw new Error('invalid_start');
      this.runID = value.runID;
      this.emit(value);
    } else {
      if (this.stopped || !object(value) || !keys(value,['type','runID']) || value.type !== 'stop' || value.runID !== this.runID) throw new Error('invalid_stop');
      this.stopped = true;
      this.emit({type: 'stop', runID: this.runID});
    }
  }
  end() {
    if (this.ended) return;
    this.ended = true;
    if (this.length) this.line();
    if (this.runID === undefined) throw new Error('missing_start');
    if (!this.stopped) { this.stopped = true; this.emit({type:'stop', runID:this.runID}); }
  }
}

export function runnerIdentity() {
  const require = createRequire(import.meta.url);
  return {protocolVersion: 1, node: process.version, playwright: require('@playwright/test/package.json').version as string,
    sourceHashes: Object.fromEntries(['alert-navigation.client.ts','alert-navigation.runner.ts'].map(file =>
      [file,createHash('sha256').update(readFileSync(new URL(file, import.meta.url))).digest('hex')]))};
}

export function navigationDeadline(cancel: () => void, force: () => void) {
  const deadline=setTimeout(cancel,295_000), hardDeadline=setTimeout(force,300_000);
  return () => {clearTimeout(deadline);clearTimeout(hardDeadline);};
}

// No exception messages/stacks or arbitrary input objects may cross this boundary.
export async function main() {
  const state = navigationState(), abort = new AbortController();
  const identity = runnerIdentity();
  let runID: string | null = null, finished = false, failure: string | undefined;
  let work: Promise<void> | undefined;
  const beganAt = Date.now();
  const emit = (message: object) => process.stdout.write(JSON.stringify(message)+'\n');
  const report = (forced = false) => {
    if (finished) return;
    finished = true;
    clearDeadline();
    process.stdin.pause();
    const failures = [...(failure ? [failure] : []), ...navigationFailures(state)];
    emit({type:'result', runID, profile:'client-only-diagnostic', capacityAcceptance:false, identity,
      beganAt, finishedAt:Date.now(), browserVersion:state.browserVersion, forcedCleanup:forced,
      status:failures.length ? 'failed' : 'complete', failures, timings:state.timings, journeySamples:state.journeySamples,
      clock:'client-wall-clock; not synchronized to backend', cleanupComplete:state.cleanupComplete,
      apiSamples:state.apiSamples, apiCycles:state.apiCycles, outcomes:state.outcomes,
      counts:{journeys:Object.values(state.timings).reduce((n,v)=>n+v.length,0), apiRequests:state.apiSamples.length}});
    process.exitCode = failures.length ? 1 : 0;
  };
  // Reserve five seconds for closing active requests/Chromium within the existing five-minute wrapper.
  const clearDeadline = navigationDeadline(() => { failure='deadline'; abort.abort(); if (!work) report(); },
    () => { failure='deadline'; report(true); process.exit(1); });
  const browserDone = (outcome: import('./alert-navigation.client.ts').Outcome) => {
    state.browserDone=true;
    emit({type:'browser-done', runID, profile:'client-only-diagnostic', capacityAcceptance:false, identity,
      outcome, timings:state.timings, journeySamples:state.journeySamples,
      journeys:Object.values(state.timings).reduce((n,v)=>n+v.length,0)});
  };
  const protocol = new NavigationProtocol(message => {
    if (message.type === 'stop') {state.stop=true;return;}
    runID=message.runID;
    work = runNavigationClient(message,state,abort.signal,browserDone)
      .catch(() => {failure ??= 'client_failed';if(!state.browserDone)browserDone({status:'rejected',code:'lane_failed'});})
      .finally(() => report());
  });
  const inputFailure = (error: unknown) => {
    const allowed=['input_closed','input_too_large','invalid_json','invalid_start','invalid_stop','missing_start'];
    failure = error instanceof Error && allowed.includes(error.message) ? error.message : 'input_failed';
    abort.abort(); process.stdin.pause();
    if (!work) report();
  };
  process.stdin.on('data', (chunk: Buffer) => {try {protocol.push(chunk);} catch(error) {inputFailure(error);}});
  process.stdin.on('end', () => {try {protocol.end();} catch(error) {inputFailure(error);}});
  process.stdin.on('error', () => inputFailure(null));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  // This final boundary also protects startup/module failure diagnostics.
  void main().catch(() => { process.stdout.write('{"type":"result","profile":"client-only-diagnostic","capacityAcceptance":false,"status":"failed","failures":["runner_failed"]}\n'); process.exitCode=1; });
}
