import {expect,it,vi} from 'vitest';
import {spawn} from 'node:child_process';
import {pathToFileURL} from 'node:url';
import {resolve} from 'node:path';
import {calculationEvidence} from '../perf/alert-status-evidence.ts';
import {navigationDeadline, writeNavigationResult} from '../perf/alert-navigation.runner.ts';

it('records optional/usable/nonusable calculation states without a ready-only gate or leaking response data',()=>{
  for(const state of [undefined,'ready','stale','failed','unavailable']) {
    const evidence=calculationEvidence({current_state:state,generation:{evaluated_through:'2026-11-14T00:00:00Z',calculation_lag_seconds:12},current:{performance:{total:'10'}},secret:'PRIVATE'});
    expect(evidence.currentState).toBe(state??null);
    expect(evidence.evaluatedThrough).toBe('2026-11-14T00:00:00Z');
    expect(evidence.calculationLagSeconds).toBe(12);
    expect(JSON.stringify(evidence)).not.toContain('PRIVATE');
  }
  expect(calculationEvidence({current_state:'PRIVATE',generation:{evaluated_through:'PRIVATE',calculation_lag_seconds:Infinity}})).toEqual({currentState:null,evaluatedThrough:null,calculationLagSeconds:null});
});

it('reserves stdout flush before the unchanged hard termination deadline',()=>{
 vi.useFakeTimers();
 try {
  const flush=vi.fn(),force=vi.fn();const clear=navigationDeadline(()=>{},force,flush);
  vi.advanceTimersByTime(299000);expect(flush).toHaveBeenCalledOnce();expect(force).not.toHaveBeenCalled();
  vi.advanceTimersByTime(1000);expect(force).toHaveBeenCalledOnce();clear();
 } finally{vi.useRealTimers();}
});

it('only completes a terminal write after the stream acknowledges it',async()=>{
 let acknowledge: ((error?:Error|null)=>void)|undefined;
 const write=vi.fn((_text:string,callback:(error?:Error|null)=>void)=>{acknowledge=callback;return false;});
 let done=false;
 const result=writeNavigationResult({status:'failed'}, {write}).then(()=>{done=true;});
 await Promise.resolve();expect(done).toBe(false);expect(write.mock.calls[0]![0]).toBe('{"status":"failed"}\n');
 acknowledge!();await result;expect(done).toBe(true);
});

it.each([false,true])('real pipe backpressure preserves the final record or obeys forced deadline (blocked=%s)',async blocked=>{
 const moduleURL=pathToFileURL(resolve('perf/alert-navigation.runner.ts')).href;
 const code=`import {writeNavigationResult} from ${JSON.stringify(moduleURL)};const hard=setTimeout(()=>process.exit(1),700);await writeNavigationResult({status:'failed',data:'x'.repeat(1024*1024)});clearTimeout(hard);process.exit(0);`;
 const child=spawn(process.execPath,['--input-type=module','-e',code],{stdio:['ignore','pipe','pipe']});
 let output='',errors='';
 child.stderr.on('data',b=>{errors+=b;});
 const exited=new Promise<number|null>((resolve,reject)=>{child.on('error',reject);child.on(blocked?'exit':'close',resolve);});
 const timer=setTimeout(()=>child.kill('SIGKILL'),3000);
 try {
  if(!blocked){await new Promise(resolve=>setTimeout(resolve,100));child.stdout.on('data',b=>{output+=b;});}
  const status=await exited;
  expect(errors).toBe('');
  expect(status).toBe(blocked?1:0);
  if(!blocked) expect(JSON.parse(output).data).toHaveLength(1024*1024);
 } finally{clearTimeout(timer);child.kill('SIGKILL');child.stdout.destroy();child.stderr.destroy();}
},5000);
