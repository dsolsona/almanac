import {useEffect,useRef,useState,type MouseEvent,type ReactNode} from 'react';
import type {AuthContext} from '../auth/client.ts';
import {Button,FilterSelect,PageHeader,Pager,RefreshButton,SearchField,StatusChip,Toolbar,ToolbarSpacer} from '../components/ui/index.ts';
import {clearQueryCache} from '../queryCache.ts';
import {journeyContentReady} from '../telemetry/client.ts';
import {serviceClient,emptySummary,type Service,type ServicePage,type Summary} from './client.ts';
import {summaryMark} from './model.ts';
import {ServiceForm} from './ServiceForm.tsx';
import {ServiceDetail} from './ServiceDetail.tsx';
import {ServiceGraph} from './ServiceGraph.tsx';
import './services.css';

export function navigateService(path:string,replace=false,state:unknown={}){if(`${location.pathname}${location.search}${location.hash}`!==path){history[replace?'replaceState':'pushState'](state,'',path);dispatchEvent(new PopStateEvent('popstate'));}}
export function ServiceIcon(){return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><path d="M17 10V6h-3M7 14v4h3M10 6h7M7 10v8"/></svg>;}
export function ServiceLink({href,children,primary=false}:{href:string;children:ReactNode;primary?:boolean}){return <a href={href} data-app-route className={`ui-button ui-button--${primary?'primary':'outline'}`}>{children}</a>;}
export function EmptyServices({title,children,action}:{title:string;children:ReactNode;action?:ReactNode}){return <div className="service-empty"><div className="service-empty-symbol"><ServiceIcon/></div><h2>{title}</h2><p>{children}</p>{action}</div>;}
export function ServiceSummaryMark({summary=emptySummary}:{summary?:Summary}){const mark=summaryMark(summary),description=summary.total?`${summary.breached} breached · ${summary.unknown} unknown · ${summary.on_track} on track`:'No objectives are defined for this service.';return <span className="service-summary-mark" title={description}><StatusChip {...mark}/><span className="sr-only">. {description}</span></span>;}
export const serviceError=(error:unknown)=>error instanceof Error?error.message:'The request could not be completed.';
export function useServicesRoute(){const [path,setPath]=useState(()=>`${location.pathname}${location.search}`);useEffect(()=>{const update=()=>setPath(`${location.pathname}${location.search}`);addEventListener('popstate',update);return()=>removeEventListener('popstate',update);},[]);return path;}
export function ServicesPage({context}:{context:AuthContext}){
 const route=useServicesRoute(),pathname=route.split('?')[0]!;
 const id=/^\/services\/([0-9a-f-]{36})(?:\/edit)?$/.exec(pathname)?.[1];
 const canWrite=context.session?.role==='administrator'||context.session?.role==='editor';
 const topLevel=pathname==='/services'||pathname==='/services/dependencies',graph=pathname==='/services/dependencies',heading=useRef<HTMLHeadingElement>(null),tableRoute=useRef('/services');
 if(pathname==='/services'){const params=new URLSearchParams(route.split('?')[1]??'');params.delete('cursor');tableRoute.current=`/services${params.size?'?'+params:''}`;}
 useEffect(()=>{if(topLevel)heading.current?.focus({preventScroll:true});},[pathname,topLevel]);
 return <div className="services-page" onClick={event=>{
  const anchor=(event.target as Element).closest<HTMLAnchorElement>('a[data-app-route]');
  if(!anchor||event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
  const next=new URL(anchor.href);if(next.origin!==location.origin)return;
  event.preventDefault();navigateService(`${next.pathname}${next.search}${next.hash}`);
 }}>
  {topLevel&&<>
  <PageHeader title="Services" headingRef={heading} actions={canWrite?<ServiceLink href="/services/new" primary><svg className="service-plus" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>New service</ServiceLink>:<span className="service-muted">View only</span>}/>
  <nav className="service-view-tabs" aria-label="Service views"><a href={tableRoute.current} data-app-route aria-current={!graph?'page':undefined}>Table</a><a href="/services/dependencies" data-app-route aria-current={graph?'page':undefined}>Graph</a></nav>
  </>}
  {pathname==='/services/new'?<ServiceForm key="new" context={context} canWrite={canWrite}/>:id&&pathname.endsWith('/edit')?<ServiceForm key={id} id={id} context={context} canWrite={canWrite}/>:id?<ServiceDetail key={id} id={id} context={context} canWrite={canWrite}/>:pathname==='/services/dependencies'?<ServiceGraph key="graph"/>:<ServiceCatalog key="catalog" route={route} canWrite={canWrite}/>}
 </div>;
}
function ServiceCatalog({route,canWrite}:{route:string;canWrite:boolean}){
 const query=new URLSearchParams(route.split('?')[1]??''),search=query.get('q')??'',owner=query.get('owner')??'',coverage=query.get('coverage')??'all',size=query.get('limit')==='20'?20:10,cursor=query.get('cursor')??'';
 const [page,setPage]=useState<ServicePage|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[refresh,setRefresh]=useState(0),[cursors,setCursors]=useState<string[]>([]),[lastRefreshed,setLastRefreshed]=useState<string>();
 useEffect(()=>{const request=new AbortController();setBusy(true);setError('');
  void serviceClient.list({q:search,owner,coverage,limit:size,cursor},request.signal).then(value=>{if(!request.signal.aborted){setPage(value);setLastRefreshed(new Date().toLocaleTimeString('en-GB',{timeZone:'UTC'}));}}).catch(failure=>{if(!request.signal.aborted)setError(serviceError(failure));}).finally(()=>{if(!request.signal.aborted){setBusy(false);journeyContentReady('management');}});
  return()=>request.abort();
 },[search,owner,coverage,size,cursor,refresh]);
 useEffect(()=>{setCursors(Array.isArray(history.state?.serviceCursors)?history.state.serviceCursors:[]);},[route]);
 const update=(field:string,value:string)=>{const next=new URLSearchParams(location.search);if(value)next.set(field,value);else next.delete(field);next.delete('cursor');setCursors([]);navigateService(`/services${next.size?'?'+next:''}`,true);};
 const goPage=(next:string,back=false)=>{const params=new URLSearchParams(location.search);if(next)params.set('cursor',next);else params.delete('cursor');const stack=back?cursors.slice(0,-1):[...cursors,cursor];setCursors(stack);navigateService(`/services${params.size?'?'+params:''}`,false,{serviceCursors:stack});};
 const all=page?.all_count??page?.total??0,withSlos=page?.with_slos_count??0,total=page?.total??0,start=cursors.length*size;
 const rowClick=(event:MouseEvent<HTMLTableRowElement>,service:Service)=>{if(event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||(event.target as Element).closest('a,button,input,select,textarea')||window.getSelection()?.toString())return;navigateService(`/services/${service.id}`);};
 return <>
  {error&&<div className="service-notice service-notice--error" role="alert">{page?'Couldn’t refresh services. Showing the last available records. ':''}{error}<Button variant="outline" onClick={()=>{clearQueryCache();setRefresh(value=>value+1);}}>Try again</Button></div>}
  <Toolbar className="services-toolbar"><SearchField icon={<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true"><circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/></svg>} id="service-search" label="Search services" shortcut placeholder="Search name, owner or label" value={search} onChange={event=>update('q',event.target.value)}/><div className="service-coverage" role="group" aria-label="SLO coverage filter">{[['all','All',all],['with','With SLOs',withSlos],['without','No SLOs',all-withSlos]].map(([value,label,count])=><Button key={value} variant="outline" aria-pressed={coverage===value} onClick={()=>update('coverage',String(value))}>{label} <span>{count}</span></Button>)}</div><FilterSelect id="service-owner" label="Owner" value={owner} options={[{value:'',label:'Any'},...(owner&&!page?.owners?.includes(owner)?[{value:owner,label:owner}]:[]),...(page?.owners??[]).map(value=>({value,label:value}))]} onChange={value=>update('owner',value)}/><ToolbarSpacer/><RefreshButton label="Refresh services" busy={busy} lastRefreshed={lastRefreshed} onClick={()=>{clearQueryCache();setRefresh(value=>value+1);}}/></Toolbar>
  {page&&<div className="service-panel" aria-busy={busy||undefined}>{page.items.length?<><table className="ui-list-table services-table"><thead><tr>{['Service','SLO status','Owner','SLOs','Depends on'].map(label=><th scope="col" key={label}>{label}</th>)}</tr></thead><tbody>{page.items.map(service=><tr key={service.id} onClick={event=>rowClick(event,service)}><td data-label="Service"><a href={`/services/${service.id}`} data-app-route className="service-row-name">{service.display_name}</a><span className="service-row-description">{service.description}</span></td><td data-label="SLO status"><ServiceSummaryMark summary={service.slo_summary}/></td><td data-label="Owner"><span className="service-owner-cell" title={service.owner||'Unassigned'}>{service.owner||'Unassigned'}</span></td><td data-label="SLOs">{service.slo_summary?.total?<a className="service-inline-link" href={`/services/${service.id}`} data-app-route>{service.slo_summary.total} SLO{service.slo_summary.total===1?'':'s'}</a>:<span className="service-muted">No SLOs</span>}</td><td data-label="Depends on"><a className="service-inline-link" href={`/services/${service.id}#dependencies`} data-app-route>{service.dependencies?.length??0}</a></td></tr>)}</tbody></table><Pager id="service-page-size" label="Services pages" position={`Showing ${start+1}–${start+page.items.length} of ${total}`} pageSize={size} pageSizes={[10,20]} onPageSizeChange={value=>update('limit',String(value))} hasPrevious={cursors.length>0} hasNext={!!page.next_cursor} busy={busy} onPrevious={()=>goPage(cursors.at(-1)??'',true)} onNext={()=>goPage(page.next_cursor)}/></>:<EmptyServices title={search||owner||coverage!=='all'?'No services match':'Your services start here'} action={search||owner||coverage!=='all'?<Button variant="outline" onClick={()=>{setCursors([]);navigateService('/services');}}>Clear filters</Button>:canWrite?<ServiceLink href="/services/new" primary>New service</ServiceLink>:undefined}>{search||owner||coverage!=='all'?'Try another name, owner or label, or clear your filters.':'Create a service to group its SLOs, share useful links and record dependencies.'}</EmptyServices>}</div>}
  {!page&&busy&&<span className="sr-only" role="status">Opening services</span>}
 </>;
}
