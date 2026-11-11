import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';

export type ManagedUser = components['schemas']['ManagedUser'];
export type SessionPolicy = components['schemas']['SessionPolicy'];
export type UserPage = components['schemas']['UserPage'];
export type CommandResult = components['schemas']['UserCommandResult'];
export type UserRole = components['schemas']['UserRole'];
export type UserWrite = components['schemas']['UserWriteRequest'];
export type UserAction = 'enable'|'disable'|'delete'|'reset-password'|'cancel-password-reset'|'replace-setup-link';

const messages:Record<string,string> = {
 unauthorized:'Your session has expired. Sign in again.', forbidden:'Your role does not allow this operation.',
 invalid_request:'Review the fields and try again.', not_found:'This user is no longer available.',
 conflict:'This user changed. Refresh and review the current version before trying again.',
 email_in_use:'A user with this email already exists.', last_administrator:'Keep at least one active admin with a local password. Activate another admin before changing this account.',
 link_unavailable:'This link has expired, has already been used or is no longer valid. Ask your Almanac admin for a new link.',
 password_policy:'Use 15–128 characters and avoid common passwords.', rate_limited:'Too many attempts. Wait before trying again.',
 user_unavailable:'Account settings are temporarily unavailable. Try again.',
 outcome_unknown:'The operation may have completed. Refresh to reconcile the result before starting a different command. A deliberate retry keeps the same command key; one-time links cannot be recovered.',
};
export class SettingsFailure extends Error {
  constructor(readonly code:string, readonly uncertain=false, readonly status=0) { super(messages[code] ?? messages.user_unavailable); }
}
function object(value:unknown):value is Record<string,unknown> { return typeof value==='object' && value!==null && !Array.isArray(value); }
export function validUser(value:unknown):value is ManagedUser {
  return object(value) && typeof value.id==='string' && /^[0-9a-f-]{36}$/.test(value.id)
    && typeof value.full_name==='string' && value.full_name.length>0 && value.full_name.length<=320
    && (value.email===null || typeof value.email==='string' && value.email.length<=254)
    && ['viewer','editor','administrator'].includes(String(value.role))
    && ['active','pending','disabled','resetting','deleted'].includes(String(value.status))
    && typeof value.version==='string' && /^[1-9][0-9]{0,18}$/.test(value.version)
    && (value.last_sign_in===null || typeof value.last_sign_in==='string' && Number.isFinite(Date.parse(value.last_sign_in)))
    && typeof value.sign_in_history_known==='boolean' && typeof value.is_last_admin==='boolean';
}
export function userETag(user:ManagedUser) { return `"user-${user.id}-${user.version}"`; }

async function request(path:string, context:AuthContext, method='GET', body?:unknown, key?:string, user?:ManagedUser, signal?:AbortSignal):Promise<unknown> {
  const headers:Record<string,string> = {};
  if (body!==undefined) { headers['Content-Type']='application/json'; headers['X-CSRF-Token']=context.csrf_token; }
  if (key) headers['Idempotency-Key']=key;
  if (user) headers['If-Match']=userETag(user);
  const bounded = signal ? AbortSignal.any([signal,AbortSignal.timeout(30_000)]) : AbortSignal.timeout(30_000);
  try {
    const response=await fetch(`/api/v1/${path}`,{method,headers,body:body===undefined?undefined:JSON.stringify(body),signal:bounded,credentials:'same-origin',mode:'same-origin',cache:'no-store',redirect:'error'});
    if (response.status===204) return null;
    const reader=response.body?.getReader(); if (!reader) throw new Error('missing body');
    const chunks:Uint8Array[]=[]; let size=0;
    try { while(true) { const part=await reader.read(); if(part.done) break; size+=part.value.byteLength; if(size>128*1024) { void reader.cancel().catch(()=>{}); throw new Error('body limit'); } chunks.push(part.value); } }
    finally { reader.releaseLock(); }
    const bytes=new Uint8Array(size); let offset=0; for(const chunk of chunks) { bytes.set(chunk,offset); offset+=chunk.length; }
    const value:unknown=JSON.parse(new TextDecoder().decode(bytes));
    if (!response.ok) {
      if (response.status>=500 && method!=='GET') throw new SettingsFailure('outcome_unknown',true,response.status);
      throw new SettingsFailure(object(value)&&typeof value.code==='string'&&Object.hasOwn(messages,value.code)?value.code:'user_unavailable',false,response.status);
    }
    return value;
  } catch(error) {
    if(error instanceof SettingsFailure) throw error;
    throw new SettingsFailure(method==='GET'?'user_unavailable':'outcome_unknown',method!=='GET');
  }
}
function checkUser(value:unknown) { if(!validUser(value)) throw new SettingsFailure('user_unavailable'); return value; }
async function command(path:string,context:AuthContext,method:string,body:unknown,key:string,user?:ManagedUser):Promise<CommandResult> {
  const value=await request(path,context,method,body,key,user);
  if(!object(value)||!validUser(value.user)||typeof value.replayed!=='boolean'||!['','setup','reset'].includes(String(value.purpose))||(value.expires_at!==null&&(typeof value.expires_at!=='string'||!Number.isFinite(Date.parse(value.expires_at))))) throw new SettingsFailure('outcome_unknown',true);
  const purpose=path==='users'||path.endsWith('/replace-setup-link')?'setup':path.endsWith('/reset-password')?'reset':'';
  if(value.purpose!==purpose||user&&value.user.id!==user.id||value.replayed&&value.link!==undefined||!value.replayed&&purpose&&value.link===undefined)throw new SettingsFailure('outcome_unknown',true);
  if(value.link!==undefined) {
    if(typeof value.link!=='string') throw new SettingsFailure('outcome_unknown',true);
    let link:URL;
    try { link=new URL(value.link); } catch { throw new SettingsFailure('outcome_unknown',true); }
    if(link.origin!==location.origin||link.username||link.password||link.pathname!=='/activate'||link.search!==''||!new RegExp(`^#alu_${purpose}_[A-Za-z0-9_-]{43}$`).test(link.hash)) throw new SettingsFailure('outcome_unknown',true);
  }
  return value as CommandResult;
}
function checkSessionPolicy(value: unknown, saving = false): SessionPolicy {
  if (!object(value) || typeof value.idle_minutes !== 'number' || !Number.isInteger(value.idle_minutes)
    || value.idle_minutes < 1 || value.idle_minutes > 720
    || typeof value.version !== 'number' || !Number.isSafeInteger(value.version) || value.version < 1) {
    throw new SettingsFailure(saving ? 'outcome_unknown' : 'user_unavailable', saving);
  }
  return { idle_minutes: value.idle_minutes, version: value.version };
}

export const settingsClient = {
  async sessionPolicy(context: AuthContext, signal?: AbortSignal) {
    return checkSessionPolicy(await request('settings/sessions', context, 'GET', undefined, undefined, undefined, signal));
  },
  async updateSessionPolicy(context: AuthContext, policy: SessionPolicy) {
    return checkSessionPolicy(await request('settings/sessions', context, 'PUT', policy), true);
  },
  async challenge(context:AuthContext,token:string,signal?:AbortSignal):Promise<components['schemas']['UserChallengeContext']> {
    const value=await request('account/activation/context',context,'POST',{token},undefined,undefined,signal);
    if(!object(value)||typeof value.full_name!=='string'||(value.email!==null&&typeof value.email!=='string')||!['setup','reset'].includes(String(value.purpose)))throw new SettingsFailure('link_unavailable');
    return value as components['schemas']['UserChallengeContext'];
  },
  async account(context:AuthContext,signal?:AbortSignal) { return checkUser(await request('account',context,'GET',undefined,undefined,undefined,signal)); },
  async user(id:string,context:AuthContext,signal?:AbortSignal) { return checkUser(await request(`users/${id}`,context,'GET',undefined,undefined,undefined,signal)); },
  async users(query:URLSearchParams,context:AuthContext,signal?:AbortSignal):Promise<UserPage> {
    const value=await request(`users?${query}`,context,'GET',undefined,undefined,undefined,signal);
    if(!object(value)||!Array.isArray(value.items)||value.items.length>20||!value.items.every(validUser)||!Number.isSafeInteger(value.total)||Number(value.total)<0||typeof value.next_cursor!=='string'||value.next_cursor.length>2048) throw new SettingsFailure('user_unavailable');
    return value as UserPage;
  },
  profile(context:AuthContext,user:ManagedUser,full_name:string,key:string) { return command('account',context,'PUT',{full_name},key,user); },
  create(context:AuthContext,body:UserWrite,key:string) { return command('users',context,'POST',body,key); },
  update(context:AuthContext,user:ManagedUser,body:UserWrite,key:string) { return command(`users/${user.id}`,context,'PUT',body,key,user); },
  action(context:AuthContext,user:ManagedUser,action:UserAction,key:string) { return command(`users/${user.id}/${action}`,context,'POST',{},key,user); },
  redeem(context:AuthContext,token:string,password:string) { return request('account/activation',context,'POST',{token,password}); },
};
