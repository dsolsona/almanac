import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';
import { Button, FormSection, TextField } from '../components/ui/index.ts';
import { settingsClient } from './client.ts';
import { journeyContentReady } from '../telemetry/client.ts';
import './settings.css';

export function ActivationPage({context}:{context:AuthContext}) {
  const token=useRef(location.hash.slice(1));
  const [info,setInfo]=useState<components['schemas']['UserChallengeContext']|null>(null);
  const [loading,setLoading]=useState(true),[pending,setPending]=useState(false),[done,setDone]=useState(false);
  const [password,setPassword]=useState(''),[confirmation,setConfirmation]=useState(''),[show,setShow]=useState(false),[error,setError]=useState('');
  const live=useRef(true),busy=useRef(false);
  const heading=useRef<HTMLHeadingElement>(null);
  useEffect(()=>{if(!loading)heading.current?.focus();},[loading,done]);
  useEffect(()=>{if(!loading)journeyContentReady('management',!info);},[loading,info]);
  useEffect(()=>{
    live.current=true;history.replaceState({},'',location.pathname);
    const controller=new AbortController();
    void settingsClient.challenge(context,token.current,controller.signal).then(value=>{if(!controller.signal.aborted)setInfo(value);},err=>{if(!controller.signal.aborted)setError(err instanceof Error?err.message:'This link is unavailable.');}).finally(()=>{if(!controller.signal.aborted)setLoading(false);});
    return()=>{live.current=false;controller.abort();token.current='';};
  },[]);
  async function submit(event:FormEvent){
    event.preventDefault();if(busy.current)return;
    if(password!==confirmation){setError('Passwords do not match.');return;}
    busy.current=true;setPending(true);setError('');const value=password;setPassword('');setConfirmation('');
    try{await settingsClient.redeem(context,token.current,value);token.current='';if(live.current)setDone(true);}
    catch(err){if(live.current)setError(err instanceof Error?err.message:'Password setup could not be confirmed. Try signing in; do not assume the link is still valid.');}
    finally{busy.current=false;if(live.current)setPending(false);}
  }
  return <main className="settings-activation" id="main"><div className="settings-page"><div className="settings-auth-wordmark">Almanac</div><div className="settings-head"><h1 ref={heading} tabIndex={-1}>{done?'Your account is ready':!loading&&!info?'This link is unavailable':info?.purpose==='reset'?'Reset your password':'Set your password'}</h1></div>
    {done?<><div className="settings-notice" role="status">Your password is set. You can now sign in with your email.</div><a className="settings-link-button settings-link-button--primary" href="/">Continue to sign in</a></>:!loading&&!info?<><div className="settings-notice settings-notice--warning" role="alert">{error}</div><a className="settings-link-button" href="/">Back to sign in</a></>:info?<><p className="settings-form-intro">Choose a password for {info.email??info.full_name}.</p>{error&&<div className="settings-notice settings-notice--error" role="alert">{error}</div>}<form onSubmit={event=>void submit(event)}><FormSection title="New password"><TextField id="setup-password" label="Password" autoComplete="new-password" type={show?'text':'password'} value={password} onChange={event=>setPassword(event.target.value)} required disabled={pending} hint="15–128 characters. Spaces are welcome. Avoid common passwords."/><TextField id="setup-confirmation" label="Confirm password" autoComplete="new-password" type={show?'text':'password'} value={confirmation} onChange={event=>setConfirmation(event.target.value)} required disabled={pending} hint="Enter the same password again."/><label className="settings-check"><input type="checkbox" checked={show} onChange={event=>setShow(event.target.checked)}/>Show passwords</label></FormSection><div className="settings-form-actions"><p className="settings-muted">Keep this password private.</p><Button type="submit" pending={pending}>Set password</Button></div></form></>:<span className="sr-only" role="status">Checking account link</span>}
  </div></main>;
}
