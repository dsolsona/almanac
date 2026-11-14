// @vitest-environment node
import {afterAll, beforeAll, expect, it, vi} from 'vitest';
import {execFileSync, spawn} from 'node:child_process';
import {mkdtempSync, readFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {createServer as httpsServer, type Server} from 'node:https';
import {createServer as httpServer} from 'node:http';
import type {AddressInfo} from 'node:net';
import {navigationTransport} from '../perf/alert-navigation.transport.ts';

let directory: string, server: Server, sink: ReturnType<typeof httpServer>, baseURL: string;
let received = 0, credentialed = 0, redirected = 0;
beforeAll(async () => {
  directory = mkdtempSync(join(tmpdir(), 'navigation-tls-'));
  for (const name of ['trusted', 'other']) execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes',
    '-keyout', join(directory, name+'.key'), '-out', join(directory, name+'.pem'), '-days', '1',
    '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1'], {stdio:'ignore',timeout:10000});
  sink=httpServer((_req,res)=>{redirected++;res.end('unexpected');});
  await new Promise<void>(resolve=>sink.listen(0,'127.0.0.1',resolve));
  server=httpsServer({key:readFileSync(join(directory,'trusted.key')),cert:readFileSync(join(directory,'trusted.pem'))},(req,res)=>{
    received++; if(req.headers.cookie?.includes('session=PRIVATE_TLS_SESSION')) credentialed++;
    if(req.url==='/redirect') {res.writeHead(302,{location:`http://127.0.0.1:${(sink.address() as AddressInfo).port}/`});res.end();}
    else {res.setHeader('Content-Type','text/html');res.end('<title>Trusted fixture</title>');}
  });
  await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));
  baseURL=`https://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
afterAll(async()=>{
  server?.closeAllConnections();sink?.closeAllConnections();
  await Promise.all([server,sink].filter(Boolean).map(s=>new Promise<void>(resolve=>s.close(()=>resolve()))));
  if(directory)rmSync(directory,{recursive:true,force:true});
});

async function probe(trust: 'trusted'|'other'|undefined) {
  // A new Node process is necessary: NODE_EXTRA_CA_CERTS is read at startup.
  const code=`
    import {chromium,request} from '@playwright/test';
    import {navigationTransport} from './perf/alert-navigation.transport.ts';
    const baseURL=process.argv[1]; const options=navigationTransport(baseURL);
    const storageState={cookies:[{name:'session',value:'PRIVATE_TLS_SESSION',domain:'127.0.0.1',path:'/',expires:-1,httpOnly:true,secure:true,sameSite:'Strict'}],origins:[]};
    const result={api:false,browser:false,redirect:null};
    const api=await request.newContext({...options.api,storageState});
    const browser=await chromium.launch(options.launch);
    try {
      try {const r=await api.get('/ok');result.api=r.status()===200;await r.dispose();}catch{}
      if(result.api){const r=await api.get('/redirect');result.redirect=r.status();await r.dispose();}
      const context=await browser.newContext({...options.context,storageState});
      try {const page=await context.newPage();await page.goto(baseURL+'/ok',{timeout:3000});result.browser=await page.title()==='Trusted fixture';}catch{}
      await context.close();
    } finally {await browser.close();await api.dispose();}
    process.stdout.write(JSON.stringify(result));
  `;
  const env={...process.env};delete env.NODE_EXTRA_CA_CERTS;
  if(trust)env.NODE_EXTRA_CA_CERTS=join(directory,trust+'.pem');
  return new Promise<{api:boolean;browser:boolean;redirect:number|null}>((resolve,reject)=>{
    const child=spawn(process.execPath,['--input-type=module','-e',code,baseURL],{env,stdio:['ignore','pipe','pipe']});
    let out='',err='';const timer=setTimeout(()=>{child.kill('SIGKILL');reject(new Error('probe_timeout'));},10000);
    child.stdout.on('data',chunk=>{out+=chunk;});child.stderr.on('data',chunk=>{err+=chunk;});
    child.on('error',error=>{clearTimeout(timer);reject(error);});
    child.on('exit',status=>{clearTimeout(timer);if(status!==0)reject(new Error(`probe_failed_${status}: ${err.slice(0,400)}`));else resolve(JSON.parse(out));});
  });
}

it('does not send credentials to an untrusted certificate, in API or browser', async()=>{
  const before=received;
  expect(await probe(undefined)).toEqual({api:false,browser:false,redirect:null});
  expect(received).toBe(before);
},15000);
it('trusts only the selected fixture certificate and refuses API downgrade redirects',async()=>{
  const before=credentialed;
  expect(await probe('trusted')).toEqual({api:true,browser:true,redirect:302});
  expect(credentialed).toBeGreaterThanOrEqual(before+3);expect(redirected).toBe(0);
},15000);
it('an unrelated trusted certificate cannot authenticate the same host',async()=>{
  const before=received;
  expect(await probe('other')).toEqual({api:false,browser:false,redirect:null});
  expect(received).toBe(before);
},15000);
it('rejects fixture trust for a different hostname before launching clients',()=>{
  vi.stubEnv('NODE_EXTRA_CA_CERTS',join(directory,'trusted.pem'));
  try {expect(()=>navigationTransport('https://unrelated.example')).toThrow('invalid_fixture_certificate');}
  finally {vi.unstubAllEnvs();}
});
