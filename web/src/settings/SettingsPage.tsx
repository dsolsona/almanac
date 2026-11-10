import { useEffect, useRef, useState, type FormEvent, type MouseEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { AuthFailure, createAuthClient } from '../auth/client.ts';
import { Button, Combobox, Dialog, FormActions, FormSection, PageHeader, Pager, RefreshButton, RoleChip, SearchField, SegmentedControl, StatusChip, TextField, ThemeChoice, Toolbar, ToolbarSpacer } from '../components/ui/index.ts';
import { settingsClient, SettingsFailure, type CommandResult, type ManagedUser, type UserAction, type UserPage, type UserRole } from './client.ts';
import { journeyContentReady } from '../telemetry/client.ts';
import './settings.css';

const roleOptions = [
  {id:'viewer',label:'Viewer',detail:'Read services, SLIs, SLOs and measurements.'},
  {id:'editor',label:'Editor',detail:'Viewer access, plus manage services, SLIs and SLOs.'},
  {id:'administrator',label:'Admin',detail:'Editor access, plus manage users, agents, datasources and credentials.'},
];
const labels:Record<string,string>={active:'Active',pending:'Pending setup',disabled:'Disabled',resetting:'Password reset'};
const defaultAuthClient=createAuthClient();
function navigate(path:string) { history.pushState({},'',path); window.dispatchEvent(new PopStateEvent('popstate')); }
function userStatus(user:ManagedUser) {
  const tone=user.status==='active'?'healthy':user.status==='disabled'?'stale':user.status==='resetting'?'warning':'none';
  return <StatusChip tone={tone} label={labels[user.status] ?? user.status} className={`settings-user-status settings-user-status--${user.status}`}/>;
}
function LastSignIn({user,now}:{user:ManagedUser;now:number}) {
  if(!user.last_sign_in) return <span className="settings-muted" title={user.sign_in_history_known?undefined:'Sign-in history unavailable'}>{user.sign_in_history_known?'Never signed in':'—'}</span>;
  const minutes=Math.max(0,Math.floor((now-Date.parse(user.last_sign_in))/60_000));
  const previousDay=Math.floor(now/86_400_000)-Math.floor(Date.parse(user.last_sign_in)/86_400_000)===1;
  const label=previousDay?'Yesterday':minutes<1?'Just now':minutes<60?`${minutes} min ago`:minutes<1440?`${Math.floor(minutes/60)} ${minutes<120?'hour':'hours'} ago`:`${Math.floor(minutes/1440)} ${minutes<2880?'day':'days'} ago`;
  const exact=new Date(user.last_sign_in).toISOString().replace('T',' ').replace('.000Z',' UTC');
  return <time dateTime={user.last_sign_in} title={exact} aria-label={`Last sign-in: ${exact}`}>{label}</time>;
}

export function SettingsPage({context,onIssuedLink,onProfileChange,onSessionChange,onAuthFailure,onCheckSession,client=defaultAuthClient}:{context:AuthContext;onIssuedLink:(result:CommandResult)=>void;onProfileChange?:(user:ManagedUser)=>void;onSessionChange?:(context:AuthContext)=>void;onAuthFailure?:(error:AuthFailure)=>void;onCheckSession?:()=>void;client?:ReturnType<typeof createAuthClient>}) {
  const authClient=useRef(client).current;
  const [path,setPath]=useState(location.pathname);
  const [record,setRecord]=useState<ManagedUser|null>(null);
  const [page,setPage]=useState<UserPage|null>(null);
  const [search,setSearch]=useState(''),[status,setStatus]=useState(''),[roleFilter,setRoleFilter]=useState('');
  const [limit,setLimit]=useState(10),[offset,setOffset]=useState(0),[refresh,setRefresh]=useState(0);
  const [cursors,setCursors]=useState<string[]>(['']);
  const [name,setName]=useState(''),[email,setEmail]=useState(''),[role,setRole]=useState<string|null>(null),[draftPath,setDraftPath]=useState('');
  const [password,setPassword]=useState(''),[confirmation,setConfirmation]=useState(''),[showPassword,setShowPassword]=useState(false);
  const [readPath,setReadPath]=useState('');
  const [readPending,setReadPending]=useState(true),[pending,setPending]=useState(false),[uncertain,setUncertain]=useState(false);
  const [error,setError]=useState(''),[message,setMessage]=useState(''),[fieldErrors,setFieldErrors]=useState<Record<string,string>>({});
  const [confirmAction,setConfirmAction]=useState<UserAction|null>(null);
  const [now,setNow]=useState(Date.now());
  const heading=useRef<HTMLHeadingElement>(null),trigger=useRef<HTMLElement>(null),cancelButton=useRef<HTMLButtonElement>(null);
  const busy=useRef(false),live=useRef(true),navigation=useRef(0),intent=useRef<{signature:string;key:string}|null>(null);
  const account=path==='/settings'||path==='/settings/account', users=path.startsWith('/settings/users'), list=path==='/settings/users', creating=path==='/settings/users/new', changingPassword=path==='/settings/password';
  const admin=context.session?.role==='administrator', id=path.split('/')[3];
  const target=record && (account?record.id===context.session?.user_id:record.id===id)?record:null;
  const blocked=pending||uncertain;
  useEffect(()=>{live.current=true;const changed=()=>{navigation.current++;setPath(location.pathname);};window.addEventListener('popstate',changed);return()=>{live.current=false;window.removeEventListener('popstate',changed);};},[]);
  useEffect(()=>{setPassword('');setConfirmation('');setConfirmAction(null);setFieldErrors({});setError('');setUncertain(false);intent.current=null;heading.current?.focus();},[path]);
  useEffect(()=>{
    const controller=new AbortController();setReadPending(true);setError('');
    if(users&&!admin||changingPassword) {setReadPath(path);setReadPending(false);return()=>controller.abort();}
    if(creating) {setRecord(null);setName('');setEmail('');setRole(null);setDraftPath(path);setReadPath(path);setReadPending(false);return()=>controller.abort();}
    const read=async()=>{
      try {
        if(list) { const query=new URLSearchParams({limit:String(limit)});if(cursors[offset/limit])query.set('cursor',cursors[offset/limit]!);if(search)query.set('q',search);if(status)query.set('status',status);if(roleFilter)query.set('role',roleFilter);const result=await settingsClient.users(query,context,controller.signal);if(!controller.signal.aborted){setPage(result);setNow(Date.now());} }
        else {const user=account?await settingsClient.account(context,controller.signal):await settingsClient.user(id??'',context,controller.signal);if(!controller.signal.aborted){setRecord(user);setName(user.full_name);setEmail(user.email??'');setRole(user.role);setDraftPath(path);if(account)onProfileChange?.(user);}}
      } catch(err) {if(!controller.signal.aborted)setError(err instanceof Error?err.message:'Account settings could not be loaded.');}
      finally {if(!controller.signal.aborted){setReadPending(false);setReadPath(path);}}
    };
    const timer=list&&search?setTimeout(()=>void read(),150):undefined;
    if(timer===undefined)void read();
    return()=>{clearTimeout(timer);controller.abort();};
  },[path,search,status,roleFilter,limit,offset,cursors,refresh,context.session?.user_id]); // callbacks and CSRF changes must not discard a draft

  useEffect(()=>{if(!readPending&&readPath===path)journeyContentReady('management',Boolean(error));},[readPending,readPath,path,error]);

  function resetPage(){setOffset(0);setCursors(['']);}
  function reload(){if(list)resetPage();setUncertain(false);intent.current=null;setRefresh(value=>value+1);}
  function keyFor(signature:string){if(intent.current?.signature!==signature)intent.current={signature,key:crypto.randomUUID()};return intent.current.key;}
  async function mutate(operation:'profile'|'create'|'update'|UserAction){
    if(busy.current)return;
    const body={full_name:name.trim(),email:email.trim(),role:role as UserRole};
    const errors:Record<string,string>={};
    if(['profile','create','update'].includes(operation)&&!body.full_name)errors.name='Enter a name.';
    if(['create','update'].includes(operation)&&!role)errors.role='Choose a role from the list.';
    if(Object.keys(errors).length){setFieldErrors(errors);return;}
    if(operation!=='create'&&!target)return;
    busy.current=true;setPending(true);setError('');setFieldErrors({});
    const generation=navigation.current;
    const key=keyFor(JSON.stringify({operation,user:target?.id,version:target?.version,...body}));
    try {
      const result=operation==='create'?await settingsClient.create(context,body,key):operation==='profile'?await settingsClient.profile(context,target!,body.full_name,key):operation==='update'?await settingsClient.update(context,target!,body,key):await settingsClient.action(context,target!,operation,key);
      if(!live.current||generation!==navigation.current)return;
      intent.current=null;setUncertain(false);setConfirmAction(null);setRecord(result.user);setMessage(operation==='delete'?`${result.user.full_name} deleted.`:operation==='profile'?'Your name has been updated.':operation==='create'?'User added.':operation==='update'?'Changes saved.':'Account access updated.');
      if(result.user.id===context.session?.user_id)onProfileChange?.(result.user);
      if(result.link)onIssuedLink(result);
      else if(result.replayed&&['create','reset-password','replace-setup-link'].includes(operation))setMessage('The original command completed. Its one-time link cannot be retrieved; generate a replacement if needed.');
      if(operation==='create')navigate(`/settings/users/${result.user.id}`);
      if(operation==='delete'){resetPage();navigate('/settings/users');}
      if(result.user.id===context.session?.user_id&&operation!=='profile') {
        const next=await authClient.context();if(live.current)onSessionChange?.(next);
      }
    }catch(err){if(live.current&&generation===navigation.current){setError(operation==='create'&&err instanceof SettingsFailure&&err.uncertain?'The operation may have completed. Retry Add user with this unchanged draft to confirm the original command. A setup link is only shown once.':err instanceof Error?err.message:'The operation could not be confirmed.');setUncertain(err instanceof SettingsFailure&&err.uncertain);if(err instanceof SettingsFailure&&err.code==='email_in_use')setFieldErrors({email:err.message});}}
    finally{busy.current=false;if(live.current)setPending(false);}
  }
  async function changePassword(event:FormEvent){
    event.preventDefault();if(busy.current)return;
    if(password!==confirmation){setFieldErrors({confirmation:'Passwords do not match.'});return;}
    busy.current=true;setPending(true);setError('');setFieldErrors({});
    const value=password;setPassword('');setConfirmation('');
    try{const next=await authClient.changePassword({new_password:value},context);if(live.current){onSessionChange?.(next);setMessage('Password changed.');navigate('/settings/account');}}
    catch(err){if(live.current){if(err instanceof AuthFailure&&err.kind==='uncertain'){if(onAuthFailure)onAuthFailure(err);else if(err.recovery)onSessionChange?.(err.recovery);}setError(err instanceof Error?err.message:'Password change could not be confirmed. Check your session before trying again.');}}
    finally{busy.current=false;if(live.current)setPending(false);}
  }
  function openConfirmation(action:UserAction,event:MouseEvent<HTMLButtonElement>){trigger.current=event.currentTarget;setConfirmAction(action);setError('');}
  function routeClick(event:MouseEvent<HTMLElement>){
    const anchor=event.target instanceof Element?event.target.closest<HTMLAnchorElement>('a[data-settings-route]'):null;
    if(anchor&&!event.defaultPrevented&&event.button===0&&!event.ctrlKey&&!event.metaKey&&!event.shiftKey&&!event.altKey){event.preventDefault();if(pending)return;setConfirmAction(null);setMessage('');navigate(anchor.pathname);}
  }
  const crumb=(current:string,isUser=false)=><nav className="settings-crumbs" aria-label="Breadcrumb"><a href="/settings/account" data-settings-route>Settings</a><span aria-hidden="true">/</span>{isUser&&<><a href="/settings/users" data-settings-route>Users</a><span aria-hidden="true">/</span></>}<span aria-current="page">{current}</span></nav>;
  const title=list?'Users':creating?'Add user':account?'Your account':changingPassword?'Change password':target?.full_name??'User details';
  const notice=error&&<div className="settings-notice settings-notice--error" role="alert">{error}{!creating&&(uncertain||!pending)&&<Button variant="link" onClick={changingPassword?onCheckSession:reload} disabled={changingPassword&&!onCheckSession}>{changingPassword?'Check session':'Refresh'}</Button>}</div>;
  const confirmTitle=target&&confirmAction?confirmAction==='delete'?`Delete ${target.full_name}?`:confirmAction==='disable'?`Disable ${target.full_name}?`:confirmAction==='enable'?`Enable ${target.full_name}?`:confirmAction==='reset-password'?`Reset ${target.full_name}’s password?`:confirmAction==='cancel-password-reset'?'Cancel password reset?':'Replace password setup link?':'';
  const confirmDescription:Record<UserAction,string>={delete:'This permanently removes the account and revokes its sessions, tokens and pending links. Past audit activity keeps its attribution. This cannot be undone.',disable:'They will be signed out immediately. Their sessions, personal tokens and pending setup or reset links will be revoked. Their profile and role are kept.',enable:'Restore this account’s access. They will need to sign in again; previous sessions and links stay revoked.','reset-password':'This signs the user out, revokes their tokens and pending links, and suspends password sign-in until the new password is set. Share the new link privately; it expires in 15 minutes.','cancel-password-reset':'The reset link will stop working. The user can sign in again with their previous password. Revoked sessions and tokens stay revoked.','replace-setup-link':'The original setup link will stop working. Share the replacement privately; it expires in 24 hours.'};
  const actionLabel:Record<UserAction,string>={delete:'Delete user',disable:'Disable user',enable:'Enable user','reset-password':'Generate link','cancel-password-reset':'Cancel reset','replace-setup-link':'Generate link'};
  const resetAction:UserAction=target?.status==='resetting'?'cancel-password-reset':target?.status==='pending'?'replace-setup-link':'reset-password';

  return <div className="settings-page" onClick={routeClick}>
    <nav className="settings-tabs" aria-label="Settings"><a href="/settings/account" data-settings-route aria-current={!users?'page':undefined}>Your account</a>{admin&&<a href="/settings/users" data-settings-route aria-current={users?'page':undefined}>Users</a>}</nav>
    {message&&<div className="settings-notice" role="status">{message}</div>}
    {changingPassword?<nav className="settings-crumbs" aria-label="Breadcrumb"><a href="/settings/account" data-settings-route>Your account</a><span aria-hidden="true">/</span><span aria-current="page">Change password</span></nav>:crumb(list||users&&!admin?'Users':title,users&&!list&&admin)}
    {account?<div className="settings-head"><h1 ref={heading} tabIndex={-1}>Your account</h1><RoleChip role={context.session?.role??'viewer'}/></div>:<PageHeader title={title} headingRef={heading} actions={list&&admin?<Button onClick={()=>{setMessage('');navigate('/settings/users/new');}}>Add user</Button>:target&&!changingPassword?userStatus(target):undefined}/>}
    {notice}
    {users&&!admin?<><div className="settings-notice settings-notice--warning"><strong>Admin access required.</strong><p>Your role can manage your own account, but cannot view or change other users.</p></div><a className="settings-link-button" href="/settings/account" data-settings-route>Your account</a></>:list?<>
      <Toolbar><SearchField id="user-search" label="Search users" placeholder="Search name or email" shortcut value={search} onChange={event=>{setSearch(event.target.value);resetPage();}}/><SegmentedControl label="User status" value={status} onChange={value=>{setStatus(value);resetPage();}} options={[{value:'',label:'All'},{value:'active',label:'Active'},{value:'pending',label:'Pending'},{value:'disabled',label:'Disabled'}]}/><select className={`settings-role-filter ${roleFilter?'is-active':''}`} aria-label="Filter by role" value={roleFilter} onChange={event=>{setRoleFilter(event.target.value);resetPage();}}><option value="">All roles</option>{roleOptions.map(option=><option key={option.id} value={option.id}>{option.label}</option>)}</select><ToolbarSpacer/><RefreshButton label="Refresh users" onClick={reload} busy={readPending}/></Toolbar>
      {page&&<div className="settings-list-frame" aria-busy={readPending}>
        {page.items.length?<div className="settings-table-scroll" role="region" aria-label="Users table" tabIndex={0}><table className="settings-table"><thead><tr><th scope="col">Name</th><th scope="col">Email</th><th scope="col">Status</th><th scope="col">Role</th><th scope="col">Last sign-in</th></tr></thead><tbody>{page.items.map(user=><tr key={user.id} onClick={event=>{if(event.button===0&&!event.metaKey&&!event.ctrlKey&&!event.shiftKey&&!event.altKey&&!(event.target as Element).closest('a,button,input,select')&&!window.getSelection()?.toString())navigate(`/settings/users/${user.id}`);}}><td><a href={`/settings/users/${user.id}`} data-settings-route>{user.full_name}{user.id===context.session?.user_id&&<span className="settings-you">You</span>}</a></td><td className="settings-email">{user.email??<span className="settings-muted">—</span>}</td><td>{userStatus(user)}</td><td><RoleChip role={user.role}/></td><td className="settings-last-sign-in"><LastSignIn user={user} now={now}/></td></tr>)}</tbody></table></div>:<div className="settings-empty"><h2>{search||status||roleFilter?'No matching users':'Add your first teammate'}</h2><p>{search||status||roleFilter?'Try another name, email or filter.':'Create an account, choose a role and share a one-time password setup link.'}</p>{(search||status||roleFilter)&&<Button variant="outline" onClick={()=>{setSearch('');setStatus('');setRoleFilter('');resetPage();}}>Clear filters</Button>}</div>}
        {page.items.length>0&&<Pager id="user-page-size" label="User pages" position={`${offset+1}–${offset+page.items.length} of ${page.total} users`} pageSize={limit} pageSizes={[10,20]} onPageSizeChange={value=>{setLimit(value);resetPage();}} hasPrevious={offset>0} hasNext={Boolean(page.next_cursor)} onPrevious={()=>setOffset(value=>Math.max(0,value-limit))} onNext={()=>{setCursors(value=>[...value.slice(0,offset/limit+1),page.next_cursor]);setOffset(value=>value+limit);}} busy={readPending}/>}
      </div>}
      {!page&&readPending&&<span className="sr-only" role="status">Opening users</span>}
    </>:changingPassword?<form onSubmit={event=>void changePassword(event)}>
      <p className="settings-form-intro">Changing your password signs out other sessions and revokes personal tokens and pending sign-in links. You stay signed in here.</p>
      <FormSection title="New password"><TextField id="new-password" label="Password" value={password} onChange={event=>setPassword(event.target.value)} type={showPassword?'text':'password'} autoComplete="new-password" required hint="15–128 characters. Spaces are welcome. Avoid common passwords." disabled={pending}/><TextField id="confirm-password" label="Confirm password" value={confirmation} onChange={event=>setConfirmation(event.target.value)} type={showPassword?'text':'password'} autoComplete="new-password" required hint="Enter the same password again." error={fieldErrors.confirmation} disabled={pending}/><label className="settings-check"><input type="checkbox" checked={showPassword} onChange={event=>setShowPassword(event.target.checked)}/>Show passwords</label></FormSection>
      <div className="settings-form-actions"><Button variant="outline" onClick={()=>navigate('/settings/account')} disabled={pending}>Cancel</Button><Button type="submit" pending={pending}>Change password</Button></div>
    </form>:draftPath===path&&(target||creating)?<form onSubmit={event=>{event.preventDefault();void mutate(account?'profile':creating?'create':'update');}}>
      <FormSection title="Profile"><div className="ui-form-grid"><TextField id="full-name" label="Full name" autoComplete="name" maxLength={160} required value={name} onChange={event=>setName(event.target.value)} error={fieldErrors.name} disabled={blocked}/><TextField id="user-email" label={account?'Sign-in email':'Email'} type="email" autoComplete="email" maxLength={254} required={!account} value={email} onChange={event=>setEmail(event.target.value)} readOnly={account} error={fieldErrors.email} hint={account?'An admin can change your email in Users.':undefined} disabled={blocked}/></div>
        {account?<ThemeChoice/>:<div className="settings-role-picker"><Combobox id="user-role" label="Role" value={role} onChange={setRole} options={roleOptions} searchLabelOnly placeholder="Search roles…" error={fieldErrors.role} disabled={blocked}/></div>}
        {!account&&target?.is_last_admin&&<div className="settings-notice settings-notice--warning">This is the last active admin with a local password. Activate another admin before changing this role, disabling or deleting this account.</div>}
      </FormSection>
      {!account&&target&&<FormSection title="Account access">
        <div className="settings-access-row"><div><h3>{target.status==='pending'?'Password setup':'Reset password'}</h3><p>{target.status==='pending'?'Setup pending. Create a replacement link if needed.':target.status==='resetting'?'Reset pending. Password sign-in is suspended until completed or cancelled.':'Create a private reset link, valid for 15 minutes.'}{target.is_last_admin?' Another active admin is required before resetting this password.':target.status==='disabled'?' Enable this user before resetting their password.':''}</p></div><Button variant="link" disabled={pending||target.is_last_admin||target.status==='disabled'} onClick={event=>openConfirmation(resetAction,event)}>{target.status==='resetting'?'Cancel password reset':target.status==='pending'?'Replace setup link':'Reset password'}</Button></div>
        <div className="settings-access-row"><div><h3>{target.status==='disabled'?'Enable user':'Disable user'}</h3><p>{target.status==='disabled'?'Restore access with the same profile and role. Revoked sessions and links stay revoked.':'Block sign-in and revoke their sessions, tokens and pending links. You can enable them again.'}</p></div><Button variant={target.status==='disabled'?'outline':'danger-outline'} disabled={pending||target.is_last_admin} onClick={event=>openConfirmation(target.status==='disabled'?'enable':'disable',event)}>{target.status==='disabled'?'Enable user':'Disable user'}</Button></div>
        <div className="settings-access-row"><div><h3>Delete user</h3><p>Remove their account and access permanently. Past activity remains attributed in audit history.</p></div><Button variant="danger-outline" disabled={pending||target.is_last_admin} onClick={event=>openConfirmation('delete',event)}>Delete user</Button></div>
        {target.is_last_admin&&<p>The last active local admin cannot be disabled or deleted.</p>}
      </FormSection>}
      <FormActions className="settings-form-actions" note={account?<Button variant="outline" onClick={()=>navigate('/settings/password')} disabled={pending}>Change password</Button>:creating?'Next: share a 24-hour password setup link.':undefined}><Button variant="outline" onClick={()=>{if(account){setName(target!.full_name);setError('');}else navigate('/settings/users');}} disabled={pending}>Cancel</Button><Button type="submit" pending={pending}>{creating?'Add user':'Save changes'}</Button></FormActions>
    </form>:readPending?<span className="sr-only" role="status">Opening account settings</span>:null}
    <Dialog open={confirmAction!==null} onClose={()=>{if(!pending)setConfirmAction(null);}} title={confirmTitle||'Confirm account action'} triggerRef={trigger} initialFocusRef={cancelButton}>
      {confirmAction&&<><p>{confirmDescription[confirmAction]}</p>{error&&<div role="alert" className="settings-notice settings-notice--error">{error}</div>}<div className="settings-dialog-actions"><Button ref={cancelButton} variant="outline" onClick={()=>setConfirmAction(null)} disabled={pending}>Cancel</Button><Button variant={confirmAction==='delete'||confirmAction==='disable'?'danger':'primary'} onClick={()=>void mutate(confirmAction)} pending={pending}>{actionLabel[confirmAction]}</Button></div></>}
    </Dialog>
  </div>;
}

export function AccountLinkDialog({issued,onClose}:{issued:CommandResult;onClose:()=>void}) {
  const [copyMessage,setCopyMessage]=useState('');
  const trigger=useRef<HTMLElement>(null),doneButton=useRef<HTMLButtonElement>(null);
  useEffect(()=>()=>{document.querySelector<HTMLElement>('#main h1')?.focus();},[]);
  async function copyLink(){try{await navigator.clipboard.writeText(issued.link!);setCopyMessage('Link copied.');}catch{setCopyMessage('Copy is unavailable. Select the link above to copy it.');}}
  return <div className="settings-page settings-link-output"><Dialog open onClose={onClose} title={issued.purpose==='reset'?'Password reset link ready':'Password setup link ready'} triggerRef={trigger} initialFocusRef={doneButton}>
    <p>{issued.user.full_name} · {issued.user.email}. Share this link privately. It expires in {issued.purpose==='reset'?'15 minutes':'24 hours'} and can be used once. No email has been sent.</p><div className="settings-one-time"><code>{issued.link}</code></div><p className="settings-muted">This link will not be available after closing this dialog.</p><div className="settings-dialog-actions"><Button variant="outline" onClick={()=>void copyLink()}>Copy link</Button><Button ref={doneButton} onClick={onClose}>Done</Button></div><p role="status">{copyMessage}</p>
  </Dialog></div>;
}
