import { afterAll, afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { AccountLinkDialog, SettingsPage as SettingsView } from '../src/settings/SettingsPage.tsx';
import type { CommandResult } from '../src/settings/client.ts';
import { AuthPage } from '../src/auth/AuthPage.tsx';
import { createAuthClient, type AuthContext } from '../src/auth/client.ts';

const id='f4e53d58-4d3d-4c4a-9e39-9394e91d5085';
const admin:AuthContext={csrf_token:'csrf',session:{user_id:id,login:'alex@example.test',role:'administrator',restricted:false}};
const profile={id,full_name:'Alex Morgan',email:'alex@example.test',role:'administrator',status:'active',version:'1',last_sign_in:null,sign_in_history_known:false,is_last_admin:true};
const response=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
const dialogDescriptors={showModal:Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,'showModal'),close:Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,'close')};
Object.defineProperty(HTMLDialogElement.prototype,'showModal',{configurable:true,value(this:HTMLDialogElement){this.setAttribute('open','');}});
Object.defineProperty(HTMLDialogElement.prototype,'close',{configurable:true,value(this:HTMLDialogElement){this.removeAttribute('open');}});
afterAll(()=>{for(const [name,descriptor]of Object.entries(dialogDescriptors)){if(descriptor)Object.defineProperty(HTMLDialogElement.prototype,name,descriptor);else Reflect.deleteProperty(HTMLDialogElement.prototype,name);}});
afterEach(()=>{cleanup();vi.unstubAllGlobals();history.replaceState({},'','/');localStorage.clear();document.documentElement.classList.remove('dark');});

function SettingsPage({context}:{context:AuthContext}) {
 const [issued,setIssued]=useState<CommandResult|null>(null);
 return <><SettingsView context={context} onIssuedLink={setIssued}/>{issued&&<AccountLinkDialog issued={issued} onClose={()=>setIssued(null)}/>}</>;
}

it.each(['/settings/account','/settings/users'])('starts the authorized read immediately on %s',path=>{
 history.replaceState({},'',path);
 const fetcher=vi.fn(()=>new Promise<Response>(()=>{}));vi.stubGlobal('fetch',fetcher);
 vi.useFakeTimers();
 try {
  render(<SettingsPage context={admin}/>);
  expect(fetcher).toHaveBeenCalledOnce();
 } finally {cleanup();vi.useRealTimers();}
});

it('puts the role chip, theme choice and password footer action on the account page',async()=>{
 history.replaceState({},'','/settings/account');
 vi.stubGlobal('fetch',vi.fn(async()=>response(profile)));
 render(<SettingsPage context={admin}/>);
 expect(await screen.findByLabelText('Full name')).toHaveValue('Alex Morgan');
 expect(screen.getByLabelText('Role: Admin')).toHaveTextContent('Admin');
 expect(screen.queryByText('Update your password. You’ll stay signed in here.')).not.toBeInTheDocument();
 expect(screen.getByRole('button',{name:'Change password'}).closest('.settings-form-actions')).toBeTruthy();
 await userEvent.click(screen.getByRole('radio',{name:/^Dark$/}));
 expect(document.documentElement).toHaveClass('dark');
 expect(localStorage.getItem('almanac-theme')).toBe('dark');
});

it('filters role names without matching the permission descriptions',async()=>{
 history.replaceState({},'','/settings/users/new');
 render(<SettingsPage context={admin}/>);
 await userEvent.type(screen.getByRole('combobox',{name:'Role'}),'view');
 const list=screen.getByRole('listbox');
 expect(within(list).getAllByRole('option')).toHaveLength(1);
 expect(within(list).getByRole('option')).toHaveTextContent('Viewer');
 await userEvent.keyboard('{ArrowDown}{Enter}');
 expect(screen.getByRole('button',{name:'Change'})).toBeVisible();
});

it('opens user details from a status cell and keeps names as real links',async()=>{
 history.replaceState({},'','/settings/users');
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>response(String(input).includes('?')?{items:[profile],total:1,next_cursor:''}:profile)));
 render(<SettingsPage context={admin}/>);
 const name=await screen.findByRole('link',{name:/Alex Morgan/});
 expect(name).toHaveAttribute('href',`/settings/users/${id}`);
 fireEvent.click(screen.getByRole('cell',{name:'Active'}));
 expect(location.pathname).toBe(`/settings/users/${id}`);
});

it('does not request or expose the user directory to an editor',()=>{
 history.replaceState({},'','/settings/users');
 const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);
 render(<SettingsPage context={{...admin,session:{...admin.session!,role:'editor'}}}/>);
 expect(screen.getByText('Admin access required.')).toBeVisible();
 expect(fetcher).not.toHaveBeenCalled();
});

it('preserves a failed creation draft and retries deliberately with the same key, without persisting the link',async()=>{
 history.replaceState({},'','/settings/users/new');
 const created={...profile,id:'00000000-0000-4000-8000-000000000001',full_name:'Sam Rivera',email:'sam@example.test',role:'viewer',status:'pending',is_last_admin:false};
 const link=`${location.origin}/activate#alu_setup_${'A'.repeat(43)}`;
 let creations=0;
 const fetcher=vi.fn(async(_input:RequestInfo|URL,init?:RequestInit)=>init?.method==='POST'?(++creations===1?response({code:'user_unavailable',message:'unsafe server details'},503):response({user:created,purpose:'setup',expires_at:'2026-11-07T14:00:00Z',replayed:false,link},201)):response(created));
 vi.stubGlobal('fetch',fetcher);
 render(<SettingsPage context={admin}/>);
 await userEvent.type(await screen.findByLabelText('Full name'),'Sam Rivera');
 await userEvent.type(screen.getByLabelText('Email',{exact:true}),'sam@example.test');
 await userEvent.type(screen.getByRole('combobox',{name:'Role'}),'view');
 await userEvent.click(screen.getByRole('option'));
 await userEvent.click(screen.getByRole('button',{name:'Add user'}));
 expect(await screen.findByRole('alert')).toHaveTextContent('The operation may have completed.');
 expect(screen.getByLabelText('Full name')).toHaveValue('Sam Rivera');
 expect(screen.getByLabelText('Full name')).toBeDisabled();
 expect(screen.queryByText('unsafe server details')).not.toBeInTheDocument();
 expect(screen.queryByRole('button',{name:/^Refresh$/})).not.toBeInTheDocument();
 expect(fetcher.mock.calls.filter(call=>call[1]?.method==='POST')).toHaveLength(1);
 await userEvent.click(screen.getByRole('button',{name:'Add user'}));
 const dialog=await screen.findByRole('dialog',{name:'Password setup link ready'});
 expect(within(dialog).getByText(link)).toBeVisible();
 const writes=fetcher.mock.calls.filter(call=>call[1]?.method==='POST');
 expect((writes[0]![1]!.headers as Record<string,string>)['Idempotency-Key']).toBe((writes[1]![1]!.headers as Record<string,string>)['Idempotency-Key']);
 expect(JSON.stringify(localStorage)).not.toContain('alu_setup_');
 await userEvent.click(within(dialog).getByRole('button',{name:'Done'}));
 expect(screen.queryByText(link)).not.toBeInTheDocument();
});

it('uses the recovered session after a password-change response is lost, without repeating the password command',async()=>{
 history.replaceState({},'','/settings/password');
 const recovered={...admin,csrf_token:'replacement-csrf'};
 let rotations=0,profileCSRF='';
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(input);
  if(path.endsWith('/auth/context'))return response(rotations?recovered:admin);
  if(path.endsWith('/auth/password')){rotations++;throw new TypeError('response lost');}
  if(path.endsWith('/account')&&init?.method==='PUT'){
   profileCSRF=(init.headers as Record<string,string>)['X-CSRF-Token']!;
   return response({user:{...profile,full_name:'Alex Updated',version:'2'},purpose:'',expires_at:null,replayed:false});
  }
  return response(profile);
 }));
 render(<AuthPage client={createAuthClient()}/>);
 await userEvent.type(await screen.findByLabelText('Password',{exact:true}),'a replacement password with spaces');
 await userEvent.type(screen.getByLabelText('Confirm password',{exact:true}),'a replacement password with spaces');
 await userEvent.click(screen.getByRole('button',{name:/^Change password$/}));
 expect(await screen.findByRole('alert')).toHaveTextContent(/may have completed/);
 expect(rotations).toBe(1);
 expect(screen.getAllByRole('button',{name:/^Check session$/}).length).toBeGreaterThan(0);
 await userEvent.click(screen.getByRole('button',{name:/^Cancel$/}));
 const name=await screen.findByLabelText('Full name');
 await userEvent.clear(name);await userEvent.type(name,'Alex Updated');
 await userEvent.click(screen.getByRole('button',{name:/^Save changes$/}));
 expect(await screen.findByRole('status')).toHaveTextContent('Your name has been updated.');
 expect(profileCSRF).toBe('replacement-csrf');
 expect(rotations).toBe(1);
});

it('keeps an own reset link available after the revoked session becomes anonymous, until dismissal',async()=>{
 history.replaceState({},'',`/settings/users/${id}`);
 const self={...profile,is_last_admin:false};
 const link=`${location.origin}/activate#alu_reset_${'R'.repeat(43)}`;
 let reset=false;
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  if(String(input).endsWith('/auth/context'))return response(reset?{csrf_token:'anonymous-csrf',session:null}:admin);
  if(init?.method==='POST'){
   reset=true;
   return response({user:{...self,status:'resetting',version:'2'},purpose:'reset',expires_at:'2026-11-06T14:15:00Z',replayed:false,link});
  }
  return response(self);
 }));
 render(<AuthPage client={createAuthClient()}/>);
 await userEvent.click(await screen.findByRole('button',{name:/^Reset password$/}));
 await userEvent.click(screen.getByRole('button',{name:/^Generate link$/}));
 await screen.findByRole('button',{name:/^Sign in$/});
 const dialog=await screen.findByRole('dialog',{name:'Password reset link ready'});
 expect(within(dialog).getByText(link)).toBeVisible();
 expect(JSON.stringify(localStorage)+JSON.stringify(sessionStorage)).not.toContain('alu_reset_');
 await userEvent.click(within(dialog).getByRole('button',{name:/^Done$/}));
 expect(screen.queryByText(link)).not.toBeInTheDocument();
 expect(screen.getByRole('button',{name:/^Sign in$/})).toBeVisible();
});

it.each(['success','failure'])('ignores a late user save %s after navigation to another loaded user',async outcome=>{
 const first={...profile,id:'00000000-0000-4000-8000-000000000001',full_name:'User A',email:'a@example.test',is_last_admin:false};
 const second={...first,id:'00000000-0000-4000-8000-000000000002',full_name:'User B',email:'b@example.test'};
 history.replaceState({},'',`/settings/users/${first.id}`);
 let finish!:(response:Response)=>void;
 const saving=new Promise<Response>(resolve=>{finish=resolve;});
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  if(init?.method==='PUT')return saving;
  return response(String(input).endsWith(second.id)?second:first);
 }));
 render(<SettingsPage context={admin}/>);
 const name=await screen.findByLabelText('Full name');
 await userEvent.clear(name);await userEvent.type(name,'User A updated');
 await userEvent.click(screen.getByRole('button',{name:/^Save changes$/}));
 history.pushState({},'',`/settings/users/${second.id}`);fireEvent.popState(window);
 await waitFor(()=>expect(screen.getByLabelText('Full name')).toHaveValue('User B'));
 await act(async()=>finish(outcome==='success'?response({user:{...first,full_name:'User A updated',version:'2'},purpose:'',expires_at:null,replayed:false}):response({code:'conflict'},409)));
 await waitFor(()=>expect(screen.getByRole('button',{name:/^Save changes$/})).toBeEnabled());
 expect(screen.getByLabelText('Full name')).toHaveValue('User B');
 expect(screen.getByRole('heading',{level:1})).toHaveTextContent('User B');
 expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('loads session settings when the same signed-in user becomes an administrator', async () => {
  history.replaceState({}, '', '/settings/sessions');
  vi.stubGlobal('fetch', vi.fn(async () => response({ idle_minutes: 480, version: 1 })));
  const viewer: AuthContext = { ...admin, session: { ...admin.session!, role: 'viewer' } };
  const view = render(<SettingsPage context={viewer}/>);
  expect(await screen.findByText('Admin access required.')).toBeVisible();
  view.rerender(<SettingsPage context={admin}/>);
  expect(await screen.findByLabelText('Inactivity timeout (minutes)')).toHaveValue(480);
  view.rerender(<SettingsPage context={viewer}/>);
  expect(screen.queryByLabelText('Inactivity timeout (minutes)')).not.toBeInTheDocument();
  expect(await screen.findByText('Admin access required.')).toBeVisible();
});

it('recovers a conflicted policy form after administrator access is restored', async () => {
  history.replaceState({}, '', '/settings/sessions');
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) =>
    init?.method === 'PUT'
      ? response({ code: 'conflict' }, 409)
      : response({ idle_minutes: 480, version: 2 })));
  const view = render(<SettingsPage context={admin}/>);
  const input = await screen.findByLabelText('Inactivity timeout (minutes)');
  fireEvent.change(input, { target: { value: '360' } });
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  expect(await screen.findByText('Session settings changed. Refresh to review the current timeout.')).toBeVisible();
  expect(input).toBeDisabled();
  const viewer: AuthContext = { ...admin, session: { ...admin.session!, role: 'viewer' } };
  view.rerender(<SettingsPage context={viewer}/>);
  view.rerender(<SettingsPage context={admin}/>);
  expect(await screen.findByLabelText('Inactivity timeout (minutes)')).toBeEnabled();
});
