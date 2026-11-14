// @vitest-environment node
import { describe, expect, it, vi } from 'vitest';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { NavigationProtocol, maxStartBytes, publicOutcome, navigationDeadline } from '../perf/alert-navigation.runner.ts';
import { continueAPICycles, runAPICycles, navigationState, navigationFailures } from '../perf/alert-navigation.client.ts';

const uuid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
function start() {
  return { type: 'start', runID: 'diagnostic-1', baseURL: 'https://example.test:18188',
    fleet: { slis: Array.from({length: 200}, (_, i) => ({id: uuid(i), good: 9999})),
      slos: Array.from({length: 200}, (_, i) => ({id: uuid(i + 200)})) },
    storageState: {cookies: [{name: 'session', value: 'PRIVATE_SESSION', domain: 'example.test', path: '/', expires: -1, httpOnly: true, secure: true, sameSite: 'Strict'}], origins: []} };
}
function consume(messages: string[]) {
  const events: unknown[] = [];
  const protocol = new NavigationProtocol(event => events.push(event));
  for (const message of messages) protocol.push(Buffer.from(message));
  return {protocol, events};
}

describe('private navigation runner protocol', () => {
  it('handles fragmented UTF-8 and EOF after a complete start without losing graceful stop', () => {
    const input = start(); input.storageState.cookies[0]!.value = '秘密';
    const bytes = Buffer.from(JSON.stringify(input) + '\n');
    const events: unknown[] = []; const protocol = new NavigationProtocol(e => events.push(e));
    for (const byte of bytes) protocol.push(Buffer.from([byte]));
    protocol.end();
    expect(events).toEqual([input, {type: 'stop', runID: input.runID}]);
  });
  it('rejects oversized input before an unbounded line can be accumulated', () => {
    const {protocol} = consume([' '.repeat(maxStartBytes)]);
    expect(() => protocol.push(Buffer.from(' '))).toThrow('input_too_large');
  });
  it('accepts the exact byte boundary and rejects one byte over, including multibyte credentials', () => {
    const input = start(); const text = JSON.stringify(input);
    const padding = maxStartBytes - Buffer.byteLength(text);
    expect(() => consume([text + ' '.repeat(padding), '\n'])).not.toThrow();
    expect(() => consume([text + ' '.repeat(padding + 1), '\n'])).toThrow('input_too_large');
  });
  it.each(['http://example.test', 'https://user:PRIVATE@example.test', 'https://example.test/path', 'https://example.test?token=PRIVATE'])('rejects non-origin/private URL %s', baseURL => {
    expect(() => consume([JSON.stringify({...start(), baseURL}) + '\n'])).toThrow('invalid_start');
  });
  it('rejects wrong fleet size, duplicate IDs and invalid good counts', () => {
    for (const mutate of [(s: ReturnType<typeof start>) => s.fleet.slis.pop(),
      (s: ReturnType<typeof start>) => {s.fleet.slos[1]!.id=s.fleet.slos[0]!.id;},
      (s: ReturnType<typeof start>) => {s.fleet.slis[0]!.good=10001;}]) {
      const s=start(); mutate(s); expect(() => consume([JSON.stringify(s)+'\n'])).toThrow('invalid_start');
    }
  });
  it('rejects credential scope outside the selected host and storage-state filenames', () => {
    const input=start(); input.storageState.cookies[0]!.domain='.test';
    expect(()=>consume([JSON.stringify(input)+'\n'])).toThrow('invalid_start');
    expect(()=>consume([JSON.stringify({...start(),storageState:'/private/auth.json'})+'\n'])).toThrow('invalid_start');
    expect(()=>consume([JSON.stringify({...start(),storageState:{cookies:[],origins:[{origin:'https://other.test',localStorage:[{name:'token',value:'PRIVATE'}]}]}})+'\n'])).toThrow('invalid_start');
  });
  it('explicit stop plus EOF produces one stop and accepts a final line without LF', () => {
    const {protocol,events}=consume([JSON.stringify(start())+'\n',JSON.stringify({type:'stop',runID:'diagnostic-1'})]);
    protocol.end();protocol.end();
    expect(events).toHaveLength(2);expect(events[1]).toEqual({type:'stop',runID:'diagnostic-1'});
    expect(()=>protocol.push(Buffer.from('x'))).toThrow('input_closed');
  });
  it('rejects duplicate start, mismatched stop, unknown fields and empty EOF', () => {
    const s=JSON.stringify(start())+'\n';
    expect(() => consume([s,s])).toThrow('input_too_large');
    expect(() => consume([s,'{"type":"start"}\n'])).toThrow('invalid_stop');
    expect(() => consume([s,'{"type":"stop","runID":"other"}\n'])).toThrow('invalid_stop');
    expect(() => consume([JSON.stringify({...start(), password:'PRIVATE'})+'\n'])).toThrow('invalid_start');
    expect(() => consume([]).protocol.end()).toThrow('missing_start');
  });
  it('never includes secret-bearing input or exception diagnostics in safe outcomes', () => {
    const secret='PRIVATE_SESSION';
    const error=new Error(`request ${secret}`, {cause: {cookie:secret}});
    expect(JSON.stringify(publicOutcome({status:'rejected',reason:error}))).toBe('{"status":"rejected","code":"lane_failed"}');
    expect(() => consume([`{"password":"${secret}"}\n`])).toThrow(/^invalid_start$/);
    expect(() => consume([`{"password":"${secret}"`]).protocol.end()).toThrow(/^invalid_json$/);
  });
});

it('real Node entrypoint rejects secret-bearing malformed input without launching workload or echoing it', async () => {
  const output = await new Promise<{code: number|null; out: string; err: string}>((resolve,reject) => {
    const child = spawn(process.execPath,[fileURLToPath(new URL('../perf/alert-navigation.runner.ts',import.meta.url))],{stdio:['pipe','pipe','pipe']});
    let out='', err='';
    const timer=setTimeout(()=>{child.kill('SIGKILL');reject(new Error('child did not stop'));},5000);
    child.stdout.on('data', chunk=>{out+=chunk;}); child.stderr.on('data', chunk=>{err+=chunk;});
    child.on('error',error=>{clearTimeout(timer);reject(error);});
    child.on('exit',code=>{clearTimeout(timer);resolve({code,out,err});});
    child.stdin.end('{"type":"start","password":"PRIVATE_PASSWORD","cookie":"PRIVATE_SESSION"}\n');
  });
  expect(output.code).toBe(1); expect(output.err).toBe('');
  expect(output.out).not.toMatch(/PRIVATE|password|cookie/);
  const result=JSON.parse(output.out);
  expect(result.failures).toContain('invalid_start');
  expect(result.counts).toEqual({journeys:0,apiRequests:0});
  expect(result.capacityAcceptance).toBe(false);
  expect(result.identity.node).toMatch(/^v26\./);
});

it('runs minimum cycles on cadence after early stop, and waits for browser completion beyond minimum', async () => {
  vi.useFakeTimers();
  try {
    const state={browserDone:false,stop:true}; const cycles:number[]=[];
    const done=runAPICycles(async cycle=>{cycles.push(cycle);},state,new AbortController().signal);
    await vi.advanceTimersByTimeAsync(90_000);
    expect(cycles).toEqual([0,1,2,3,4,5,6,7,8,9]);
    await vi.advanceTimersByTimeAsync(10_000); expect(cycles).toHaveLength(11);
    state.browserDone=true;
    await vi.advanceTimersByTimeAsync(10_000); await done;
    expect(cycles).toHaveLength(11);
  } finally {vi.useRealTimers();}
});

it('cancels a pending cadence wait promptly and never retries a failed cycle', async () => {
  vi.useFakeTimers();
  try {
    const abort=new AbortController();let calls=0;
    const done=runAPICycles(async()=>{calls++;},{browserDone:true,stop:false},abort.signal);
    const observed=expect(done).rejects.toThrow();
    await vi.advanceTimersByTimeAsync(1); abort.abort();await observed;expect(calls).toBe(1);
    await expect(runAPICycles(async()=>{throw new Error('cycle failed');},{browserDone:true,stop:true},new AbortController().signal)).rejects.toThrow('cycle failed');
  } finally {vi.useRealTimers();}
});

it('partial/slow/unsuccessful work remains a diagnostic failure', () => {
  const state=navigationState();state.timings['list-cold']!.push(1000);
  state.apiSamples.push({user:0,cycle:0,name:'overview',beganAt:1,finishedAt:2,milliseconds:1,status:503});
  expect(navigationFailures(state)).toEqual(expect.arrayContaining(['incomplete_lanes','incomplete_api_cycles','list-cold_count','list-cold_latency','overview_count','overview_status']));
});

it('reserves cleanup inside five minutes and clears both deadlines when completed', () => {
  vi.useFakeTimers();
  try {
    const cancel=vi.fn(),force=vi.fn();const clear=navigationDeadline(cancel,force);
    vi.advanceTimersByTime(294999);expect(cancel).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);expect(cancel).toHaveBeenCalledOnce();expect(force).not.toHaveBeenCalled();
    vi.advanceTimersByTime(5000);expect(force).toHaveBeenCalledOnce();clear();
    const stopped=vi.fn();navigationDeadline(stopped,stopped)();
    vi.advanceTimersByTime(300000);expect(stopped).not.toHaveBeenCalled();
  } finally {vi.useRealTimers();}
});

it('graceful stop cannot truncate minimum cycles or the browser lane, but releases after both', () => {
  expect(continueAPICycles(9, true, true)).toBe(true);
  expect(continueAPICycles(10, false, true)).toBe(true);
  expect(continueAPICycles(10, true, false)).toBe(true);
  expect(continueAPICycles(10, true, true)).toBe(false);
});
