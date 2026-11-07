import {useEffect,useRef,useState,type FormEvent} from 'react';
import type {AuthContext} from '../auth/client.ts';
import {Button,Combobox,Dialog,FormActions,FormSection,LabelChips,PageHeader,TextField,TextareaField,type ComboboxOption} from '../components/ui/index.ts';
import {serviceClient,SloFailure,type Service,type ServiceWrite,type ServiceLink} from './client.ts';
import {labelPairs,labelsObject,labelKeyPattern,suggestName,validateService,validLink} from './model.ts';
import {navigateService,serviceError} from './ServicesPage.tsx';

type RetainedDraft={draft:ServiceWrite;etag:string;labelEntry:string;linkName:string;linkURL:string;key:string;manualName:boolean;uncertain:{body:ServiceWrite;key:string}|null;selected:Service[]};
// shortcut: keep the latest 20 route drafts in memory; use durable storage if cross-tab recovery is required.
const serviceDrafts=new Map<string,RetainedDraft>();
const blank:ServiceWrite={name:'',display_name:'',description:'',owner:'',contact:'',labels:{},links:[],dependencies:[]};
export function ServiceForm({id,context,canWrite}:{id?:string;context:AuthContext;canWrite:boolean}){
 const draftKey=`${context.session?.user_id}:${id??'new'}`,retained=useRef(serviceDrafts.get(draftKey)),discarded=useRef(false);
 const [savedName,setSavedName]=useState(''),[draft,setDraft]=useState<ServiceWrite>(retained.current?.draft??blank),[loaded,setLoaded]=useState(!id),[etag,setEtag]=useState(retained.current?.etag??''),[error,setError]=useState(''),[fields,setFields]=useState<Record<string,string>>({}),[busy,setBusy]=useState(false),[dirty,setDirty]=useState(!!retained.current),[discard,setDiscard]=useState(false),[reload,setReload]=useState(0);
 const [options,setOptions]=useState<Service[]>([]),[depSearch,setDepSearch]=useState(''),[optionError,setOptionError]=useState(''),[labelEntry,setLabelEntry]=useState(retained.current?.labelEntry??''),[linkName,setLinkName]=useState(retained.current?.linkName??''),[linkURL,setLinkURL]=useState(retained.current?.linkURL??''),[uncertain,setUncertain]=useState<{body:ServiceWrite;key:string}|null>(retained.current?.uncertain??null);
 const key=useRef(retained.current?.key??crypto.randomUUID()),manualName=useRef(retained.current?.manualName??!!id),heading=useRef<HTMLHeadingElement>(null),labelInput=useRef<HTMLInputElement>(null),cancel=useRef<HTMLButtonElement>(null),selectedRecords=useRef<Map<string,Service>>(new Map(retained.current?.selected.map(service=>[service.id,service])));
 const mounted=useRef(true);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;};},[]);
 const snapshot=useRef({draft,etag,labelEntry,linkName,linkURL,uncertain,dirty});snapshot.current={draft,etag,labelEntry,linkName,linkURL,uncertain,dirty};
 useEffect(()=>()=>{const current=snapshot.current;if(current.dirty&&!discarded.current){serviceDrafts.delete(draftKey);serviceDrafts.set(draftKey,{...current,key:key.current,manualName:manualName.current,selected:[...selectedRecords.current.values()]});if(serviceDrafts.size>20)serviceDrafts.delete(serviceDrafts.keys().next().value!);}else serviceDrafts.delete(draftKey);},[draftKey]);
 const blocked=busy||!!uncertain||!canWrite;
 useEffect(()=>{heading.current?.focus({preventScroll:true});},[]);
 useEffect(()=>{if(!id)return;const request=new AbortController();setError('');void serviceClient.get(id,request.signal).then(result=>{if(request.signal.aborted)return;const value=result.value;setSavedName(value.display_name);if(!retained.current){setDraft({name:value.name,display_name:value.display_name,description:value.description??'',owner:value.owner??'',contact:value.contact??'',labels:value.labels??{},links:value.links??[],dependencies:value.dependencies??[]});setEtag(result.etag);setDirty(false);}setLoaded(true);for(const reference of value.dependency_services??[])selectedRecords.current.set(reference.id,{...reference,version:'1',created_at:'',updated_at:''});}).catch(failure=>{if(!request.signal.aborted)setError(serviceError(failure));});return()=>request.abort();},[id,reload]);
 useEffect(()=>{const request=new AbortController();setOptionError('');void serviceClient.list({q:depSearch,limit:50},request.signal).then(page=>{if(!request.signal.aborted){setOptions(page.items);for(const record of page.items)selectedRecords.current.set(record.id,record);}}).catch(failure=>{if(!request.signal.aborted)setOptionError(serviceError(failure));});return()=>request.abort();},[depSearch]);
 useEffect(()=>{if(!dirty)return;const warn=(event:BeforeUnloadEvent)=>event.preventDefault();addEventListener('beforeunload',warn);return()=>removeEventListener('beforeunload',warn);},[dirty]);
 const change=<K extends keyof ServiceWrite>(field:K,value:ServiceWrite[K])=>{setDraft(old=>({...old,[field]:value}));setDirty(true);setFields(old=>({...old,[field]:''}));};
 function addLabel():boolean{
  if(!labelEntry.trim())return true;
  const index=labelEntry.indexOf(':'),key=labelEntry.slice(0,index).trim(),value=labelEntry.slice(index+1).trim(),pairs=labelPairs(draft.labels);
  let reason='';if(index<1||!labelKeyPattern.test(key)||!value||[...value].length>256)reason='Use key:value, for example team:store. Keys can contain letters, numbers, dots, underscores and hyphens.';
  else if(pairs.some(pair=>pair[0]===key&&pair[1]===value))reason='This label is already added.';else if(pairs.length>=100)reason='Use up to 100 labels.';
  setFields(old=>({...old,labels:reason}));if(reason){labelInput.current?.focus();return false;}
  change('labels',labelsObject([...pairs,[key,value]]));setLabelEntry('');labelInput.current?.focus();return true;
 }
 function addLink():boolean{
  if(!linkName.trim()&&!linkURL.trim())return true;
  const link:ServiceLink={name:linkName.trim(),url:linkURL.trim()};let reason='';
  if(!link.name)reason='Give this link a name.';else if(!validLink(link))reason='Enter an http:// or https:// URL without credentials.';else if((draft.links??[]).some(other=>other.name===link.name&&other.url===link.url))reason='This link is already added.';else if((draft.links??[]).length>=50)reason='Use up to 50 links.';
  setFields(old=>({...old,links:reason}));if(reason)return false;
  change('links',[...(draft.links??[]),link]);setLinkName('');setLinkURL('');return true;
 }
 async function save(event?:FormEvent){
  event?.preventDefault();if(busy||!canWrite)return;
  let body=uncertain?.body??{...draft,name:draft.name.trim(),display_name:draft.display_name.trim(),owner:draft.owner?.trim(),contact:draft.contact?.trim()};
  if(!uncertain){
   if(labelEntry.trim()||linkName.trim()||linkURL.trim()){
    if(labelEntry.trim()&&!addLabel())return;if((linkName.trim()||linkURL.trim())&&!addLink())return;
    body={...body,labels:labelEntry.trim()?labelsObject([...labelPairs(draft.labels),[labelEntry.slice(0,labelEntry.indexOf(':')).trim(),labelEntry.slice(labelEntry.indexOf(':')+1).trim()]]):draft.labels,links:linkName.trim()||linkURL.trim()?[...(draft.links??[]),{name:linkName.trim(),url:linkURL.trim()}]:draft.links};
   }
   const validation=validateService(body);setFields(validation);if(Object.keys(validation).length){setError('Check the highlighted fields.');return;}
  }
  setBusy(true);setError('');
  try{const result=id?await serviceClient.update(id,body,etag,context):await serviceClient.create(body,context,uncertain?.key??key.current);if(!mounted.current)return;discarded.current=true;serviceDrafts.delete(draftKey);setDirty(false);setUncertain(null);navigateService(`/services/${result.value.id}`);}
  catch(failure){if(!mounted.current)return;if(failure instanceof SloFailure&&failure.uncertain)setUncertain({body,key:uncertain?.key??key.current});setError(failure instanceof SloFailure&&failure.status===409?'This service changed while you were editing, or its identifier is already in use. Your draft is still here.':serviceError(failure));}
  finally{if(mounted.current)setBusy(false);}
 }
 async function reconcile(){
  if(!id||busy)return;setBusy(true);setError('');
  try{const result=await serviceClient.get(id);if(!mounted.current)return;const saved=result.value,body=uncertain?.body??draft;
   const pairs=(labels:ServiceWrite['labels'])=>labelPairs(labels).map(pair=>JSON.stringify(pair)).sort();
   const matches=saved.name===body.name&&saved.display_name===body.display_name&&saved.description===(body.description??'')&&saved.owner===(body.owner??'')&&saved.contact===(body.contact??'')&&JSON.stringify(pairs(saved.labels))===JSON.stringify(pairs(body.labels))&&JSON.stringify(saved.links??[])===JSON.stringify(body.links??[])&&JSON.stringify([...(saved.dependencies??[])].sort())===JSON.stringify([...(body.dependencies??[])].sort());
   if(matches){discarded.current=true;serviceDrafts.delete(draftKey);navigateService(`/services/${id}`);return;}
   setEtag(result.etag);setSavedName(saved.display_name);setUncertain(null);setError('The saved service differs from your draft. Review your retained changes before saving.');
  }catch(failure){if(mounted.current)setError(serviceError(failure));}finally{if(mounted.current)setBusy(false);}
 }
 const selected=(draft.dependencies??[]).map(value=>selectedRecords.current.get(value)).filter((value):value is Service=>!!value);
 const asOption=(service:Service):ComboboxOption=>({id:service.id,label:service.display_name,detail:`${service.owner||'Unassigned'} · ${service.description??''}`,keywords:service.name});
 const destination=id?`/services/${id}`:'/services';
 return <>
  {id&&loaded&&<nav className="app-breadcrumbs service-form-breadcrumb" aria-label="Breadcrumb"><a href="/services" data-app-route>Services</a><span>/</span><a href={`/services/${id}`} data-app-route>{savedName}</a><span>/</span><span>Edit</span></nav>}
  <PageHeader title={id?loaded?`Edit ${savedName}`:'Edit service':'New service'} headingRef={heading} breadcrumb={id?undefined:{href:'/services',label:'Services',current:'New service'}}/>
  {error&&<div className="service-notice service-notice--error" role="alert"><div>{error}</div>{id&&uncertain&&<Button variant="outline" pending={busy} onClick={()=>void reconcile()}>Check saved service</Button>}{id&&!uncertain&&<Button variant="outline" onClick={()=>{retained.current=undefined;serviceDrafts.delete(draftKey);setReload(value=>value+1);}}>Reload saved service</Button>}</div>}
  {loaded?<form className="service-form" onSubmit={save} noValidate><fieldset disabled={blocked}>
   <FormSection step={1} title="Identity"><div className="ui-form-grid"><TextField id="service-display-name" label="Display name" required autoComplete="off" value={draft.display_name} error={fields.display_name} onChange={event=>{const value=event.target.value;change('display_name',value);if(!manualName.current)change('name',suggestName(value));}}/><TextField id="service-identifier" label="Identifier" required autoComplete="off" value={draft.name} error={fields.name} hint="Lowercase letters, numbers and hyphens." onChange={event=>{manualName.current=true;change('name',event.target.value);}}/></div><TextareaField id="service-description" label="Description" optional rows={3} placeholder="What does this service do?" value={draft.description} error={fields.description} onChange={event=>change('description',event.target.value)}/></FormSection>
   <FormSection step={2} title="Ownership"><div className="ui-form-grid"><TextField id="service-owner" label="Owner" optional placeholder="e.g. Store experience" value={draft.owner} error={fields.owner} onChange={event=>change('owner',event.target.value)}/><TextField id="service-contact" label="Contact" optional placeholder="Team email or support channel" value={draft.contact} error={fields.contact} onChange={event=>change('contact',event.target.value)}/></div></FormSection>
   <FormSection step={3} title="Labels"><div className="ui-field"><label className="ui-label" htmlFor="label-entry">Labels</label><div className="service-token-field"><LabelChips labels={labelPairs(draft.labels)} onRemove={index=>{change('labels',labelsObject(labelPairs(draft.labels).filter((_,i)=>i!==index)));labelInput.current?.focus();}} disabled={blocked}/><input ref={labelInput} id="label-entry" value={labelEntry} onChange={event=>{setLabelEntry(event.target.value);setDirty(true);}} onKeyDown={event=>{if(event.key==='Enter'){event.preventDefault();addLabel();}}} placeholder="team:store" autoComplete="off" aria-describedby="label-entry-hint label-entry-error" aria-invalid={!!fields.labels||undefined}/><button type="button" className="service-token-add" onClick={addLabel}>Add</button></div><p className="ui-hint" id="label-entry-hint">Type key:value and press Enter. Use a key again to add another value.</p>{fields.labels&&<p className="ui-error" id="label-entry-error" role="alert">{fields.labels}</p>}</div></FormSection>
   <FormSection step={4} title="Links">{(draft.links??[]).length>0&&<ul className="service-added-links">{draft.links!.map((link,index)=><li key={`${link.name}:${link.url}`}><div><a href={link.url} target="_blank" rel="noopener noreferrer">{link.name} ↗</a><span>{link.url}</span></div><button type="button" className="service-remove" aria-label={`Remove link ${link.name}`} onClick={()=>change('links',draft.links!.filter((_,i)=>i!==index))}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button></li>)}</ul>}<div className="service-link-composer"><TextField id="service-link-name" label="Name" placeholder="e.g. Runbook" value={linkName} onChange={event=>{setLinkName(event.target.value);setDirty(true);}} onKeyDown={event=>{if(event.key==='Enter'){event.preventDefault();if(!linkURL)document.getElementById('service-link-url')?.focus();else addLink();}}}/><TextField id="service-link-url" label="URL" placeholder="https://…" value={linkURL} error={fields.links} onChange={event=>{setLinkURL(event.target.value);setDirty(true);setFields(old=>({...old,links:''}));}} onKeyDown={event=>{if(event.key==='Enter'){event.preventDefault();addLink();}}}/><Button variant="outline" onClick={addLink}><svg className="service-plus" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>Add link</Button></div><p className="service-link-hint">Press Enter to add the link.</p></FormSection>
   <FormSection step={5} title="Dependencies"><Combobox id="service-dependencies" label="Search dependencies" value={null} options={options.filter(service=>service.id!==id).map(asOption)} onChange={value=>{if(value&&!draft.dependencies?.includes(value))change('dependencies',[...(draft.dependencies??[]),value]);}} onSearch={setDepSearch} multiple={{selected:selected.map(asOption),onRemove:value=>change('dependencies',draft.dependencies?.filter(id=>id!==value)),removeLabel:option=>`Remove dependency ${option.label}`}} placeholder="Search services or owners…" error={fields.dependencies||optionError} disabled={blocked}/></FormSection>
  </fieldset><FormActions><Button variant="outline" ref={cancel} onClick={()=>dirty?setDiscard(true):navigateService(destination)}>Cancel</Button>{canWrite?<Button type="submit" pending={busy}>{uncertain?'Retry the same save':id?'Save changes':'Create service'}</Button>:<span className="service-muted">Editor access is required to save.</span>}</FormActions></form>:!error&&<span className="sr-only" role="status">Opening service</span>}
  <Dialog open={discard} title="Discard service changes?" onClose={()=>setDiscard(false)} triggerRef={cancel}><p>Your changes have not been saved.</p><div className="service-dialog-actions"><Button variant="outline" onClick={()=>setDiscard(false)}>Keep editing</Button><Button variant="danger" onClick={()=>{discarded.current=true;serviceDrafts.delete(draftKey);setDirty(false);navigateService(destination);}}>Discard changes</Button></div></Dialog>
 </>;
}
