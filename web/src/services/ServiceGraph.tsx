import {useEffect,useId,useMemo,useState} from 'react';
import {Button,StatusChip} from '../components/ui/index.ts';
import {serviceClient,emptySummary,type Service} from './client.ts';
import {ServiceLink,ServiceSummaryMark,serviceError} from './ServicesPage.tsx';
import {RelationshipList} from './ServiceDetail.tsx';
import {summaryMark} from './model.ts';
import {useGraphCamera} from './useGraphCamera.ts';

const graphText=(value:string,limit:number)=>[...value].length>limit?[...value].slice(0,limit-1).join('')+'…':value;
type Point=readonly [number,number];
function networkLayout(records:Service[],root:Service,grouped:boolean){
 const consumers=records.filter(service=>service.dependencies?.includes(root.id)),providers=(root.dependencies??[]).map(id=>records.find(service=>service.id===id)).filter((value):value is Service=>!!value);
 const direct=new Set([root.id,...consumers.map(service=>service.id),...providers.map(service=>service.id)]),other=records.filter(service=>!direct.has(service.id));
 const ordered=[...consumers,root,...providers,...other].filter((service,index,array)=>array.findIndex(other=>other.id===service.id)===index);
 const positions:Record<string,Point>={};let width=1040,height=600;
 const owners=[...new Set(ordered.map(service=>service.owner??''))];
 if(grouped){width=Math.max(1040,owners.length*345+5);for(const [group,owner]of owners.entries()){
  let members=ordered.filter(service=>(service.owner??'')===owner);if(members.includes(root)&&members.length===3)members=[...members.filter(service=>service!==root).slice(0,1),root,...members.filter(service=>service!==root).slice(1)];
  else if(!members.includes(root))members.sort((a,b)=>(b.dependencies?.length??0)-(a.dependencies?.length??0)||a.display_name.localeCompare(b.display_name));
  members.forEach((service,index)=>positions[service.id]=[175+group*345,members.length===2?190+index*210:125+index*170]);height=Math.max(height,225+(members.length-1)*170);
 }}else{
  positions[root.id]=[510,280];
  consumers.forEach((service,index)=>positions[service.id]=consumers.length===2?([[160,160],[175,410]] as const)[index]!:[160,120+index*170]);
  providers.forEach((service,index)=>positions[service.id]=providers.length===3?([[805,110],[870,315],[730,485]] as const)[index]!:[850,120+index*170]);
  other.forEach((service,index)=>positions[service.id]=index===0?[505,70]:[180+((index-1)%3)*340,650+Math.floor((index-1)/3)*180]);
  height=Math.max(600,consumers.length*170+80,providers.length*170+80,other.length>1?790+Math.floor((other.length-2)/3)*180:600);
 }
 return {positions,width,height,owners,ordered};
}
export function ServiceGraph(){
 const [records,setRecords]=useState<Service[]>([]),[next,setNext]=useState(''),[total,setTotal]=useState(0),[error,setError]=useState(''),[busy,setBusy]=useState(false),[design,setDesign]=useState<'a'|'b'|'c'>('a');
 const initial=new URLSearchParams(location.search).get('service_id')??'', [rootID,setRootID]=useState(initial),[selectedID,setSelectedID]=useState(initial);
 const marker=`service-map-${useId().replaceAll(':','')}`;
 async function load(cursor=''){
  setBusy(true);setError('');try{const page=await serviceClient.list({limit:200,cursor});setRecords(old=>cursor?[...old,...page.items]:page.items);setNext(page.next_cursor);setTotal(page.total??page.items.length);if(!cursor&&!rootID){setRootID(page.items[0]?.id??'');setSelectedID(page.items[0]?.id??'');}}catch(failure){setError(serviceError(failure));}finally{setBusy(false);}
 }
 useEffect(()=>{let active=true;const request=new AbortController();setBusy(true);void serviceClient.list({limit:200},request.signal).then(page=>{if(!active)return;setRecords(page.items);setNext(page.next_cursor);setTotal(page.total??page.items.length);const root=page.items.some(service=>service.id===initial)?initial:page.items[0]?.id??'';setRootID(root);setSelectedID(root);}).catch(failure=>{if(active)setError(serviceError(failure));}).finally(()=>{if(active)setBusy(false);});return()=>{active=false;request.abort();};},[]);
 const root=records.find(service=>service.id===rootID)??records[0],selected=records.find(service=>service.id===selectedID)??root;
 const layout=useMemo(()=>root?networkLayout(records,root,design==='b'):null,[records,root,design]);
 const edges=useMemo(()=>(layout?.ordered??records).flatMap(service=>(service.dependencies??[]).filter(id=>records.some(other=>other.id===id)).map(id=>[service.id,id] as const)),[records,layout]);
 const nearby=new Set(edges.filter(pair=>pair.includes(selectedID)).flat()),camera=useGraphCamera(layout);
 const attention=records.filter(service=>service.slo_summary?.state==='breached'||service.slo_summary?.state==='unknown').sort((a,b)=>(a.slo_summary?.state==='breached'?0:1)-(b.slo_summary?.state==='breached'?0:1)||a.display_name.localeCompare(b.display_name)||a.id.localeCompare(b.id));
 function focus(ids:string[],center?:Point){
  if(!layout)return;const related=new Set(ids);for(const [from,to]of edges)if(ids.includes(from)||ids.includes(to)){related.add(from);related.add(to);}
  const points=[...related].map(id=>layout.positions[id]).filter((point):point is Point=>!!point);if(!points.length)return;
  const xs=points.map(point=>point[0]),ys=points.map(point=>point[1]),cx=center?.[0]??(Math.min(...xs)+Math.max(...xs))/2,cy=center?.[1]??(Math.min(...ys)+Math.max(...ys))/2;
  const width=2*Math.max(...xs.map(x=>Math.abs(x-cx)))+280,height=2*Math.max(...ys.map(y=>Math.abs(y-cy)))+220;
  camera.fit({x:cx-width/2,y:cy-height/2,width,height});
 }
 function findService(id:string){setSelectedID(id);focus([id],layout?.positions[id]);}
 return <>
  <div className="service-graph-controls"><div className="service-graph-variants" role="group" aria-label="Dependency layout">{([['a','Service focus'],['b','Owner groups'],['c','List & map']]as const).map(([id,label])=><Button variant={design===id?'selected':'outline'} key={id} aria-pressed={design===id} onClick={()=>setDesign(id)}>{label}</Button>)}</div><div className="service-graph-legend" role="group" aria-label="Service SLO status"><StatusChip tone="healthy" label="On track"/><StatusChip tone="breached" label="Breached"/><span title="Unknown includes stale, incomplete or unavailable SLO results."><StatusChip tone="stale" label="Unknown"/></span><StatusChip tone="none" label="No SLOs"/></div></div>
  {error&&<div className="service-notice service-notice--error" role="alert">{error}<Button variant="outline" onClick={()=>load()}>Try again</Button></div>}
  {selected&&layout?<section className="service-panel"><div className="graph-toolbar"><label>Find a service <select value={selected.id} onChange={event=>findService(event.target.value)}>{layout.ordered.map(service=><option value={service.id} key={service.id}>{service.display_name}</option>)}</select></label><span className="map-legend">A → B means A depends on B</span></div>
   <div className="graph-camera-toolbar"><div className="graph-camera-actions"><Button variant="outline" onClick={()=>camera.fit()}>Fit all</Button><Button variant="outline" onClick={()=>focus([selected.id],layout.positions[selected.id])}>Focus selected</Button><Button variant="outline" disabled={!attention.length} onClick={()=>{setSelectedID(attention[0]!.id);focus(attention.map(service=>service.id));}}>Needs attention</Button></div><div className="graph-camera-zoom" role="group" aria-label="Graph zoom controls"><Button variant="outline" aria-label="Zoom out" disabled={!camera.canZoomOut} onClick={()=>camera.zoom(1/1.25)}>−</Button><output aria-label="Graph zoom">{Math.round(camera.scale*100)}%</output><Button variant="outline" aria-label="Zoom in" disabled={!camera.canZoomIn} onClick={()=>camera.zoom(1.25)}>+</Button></div><p className="graph-camera-hint" id={`${marker}-help`}>Drag to pan · Scroll to zoom · Arrow keys pan, +/− zoom, 0 fits all</p></div>
   <div className={`map-desktop ${design==='c'?'map-split':''}`}>{design==='c'&&<div className="map-service-list">{layout.ordered.map(service=><button type="button" key={service.id} onClick={()=>findService(service.id)} aria-pressed={selected.id===service.id}><strong>{service.display_name}</strong><small>{service.owner&&<>{service.owner} · </>}{service.slo_summary?.total??0} SLOs</small></button>)}</div>}
    <div className={`map-stage service-graph-viewport ${camera.dragging?'is-dragging':''}`}><svg ref={camera.ref} {...camera.events} className="service-network" viewBox={`0 0 ${camera.size.width} ${camera.size.height}`} role="group" tabIndex={0} aria-describedby={`${marker}-help`} aria-label="Service dependency map. Arrows point from a service to the service it depends on."><g className="service-camera-scene" transform={camera.transform}>
     <defs>{['','-active'].map(suffix=><marker key={suffix} id={marker+suffix} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M1 1 9 5 1 9" fill="none" stroke={`var(--${suffix?'selection':'border-strong'})`} strokeWidth="1.5"/></marker>)}</defs>
     {design==='b'&&layout.owners.map((owner,index)=><g key={owner}><ellipse className="map-group" cx={175+index*345} cy={layout.height/2} rx="155" ry={layout.height/2-30}/>{owner&&<text className="map-caption" textAnchor="middle" x={175+index*345} y="50">{owner}</text>}</g>)}
     {edges.map(([from,to])=>{const [x,y]=layout.positions[from]!,[tx,ty]=layout.positions[to]!,direction=tx>=x?1:-1,fromR=from===selected.id?54:44,toR=to===selected.id?54:44,sx=x+direction*fromR,ex=tx-direction*toR,bend=Math.max(70,Math.abs(ex-sx)/2),active=from===selected.id||to===selected.id,d=tx===x?`M${x+fromR},${y} C${x+fromR+100},${y} ${tx+toR+100},${ty} ${tx+toR},${ty}`:`M${sx},${y} C${sx+direction*bend},${y} ${ex-direction*bend},${ty} ${ex},${ty}`;return <path key={`${from}:${to}`} className={`map-edge ${active?'active':''}`} d={d} markerEnd={`url(#${marker}${active?'-active':''})`}/>;})}
     {layout.ordered.map(service=>{const [x,y]=layout.positions[service.id]!,active=service.id===selected.id,r=active?42:32,count=service.slo_summary?.total??0,status=summaryMark(service.slo_summary??emptySummary),state=service.slo_summary?.state??'no_slos';return <g className={`map-node ${active?'selected':nearby.has(service.id)?'nearby':'quiet'}`} key={service.id} data-id={service.id} data-slo-state={state} role="button" tabIndex={0} aria-pressed={active} aria-label={`Select ${service.display_name}, ${status.label}, ${count} SLO${count===1?'':'s'}`} transform={`translate(${x} ${y})`} onFocus={()=>camera.reveal([x,y])} onClick={event=>{setSelectedID(service.id);event.currentTarget.focus({preventScroll:true});}} onKeyDown={event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();setSelectedID(service.id);}}}><circle className="map-node-halo" r={r+22}/><circle className="map-node-orbit" r={r+11}/><circle className="map-node-core" r={r}/><svg x="-12" y="-12" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><path d="M17 10V6h-3M7 14v4h3M10 6h7M7 10v8"/></svg>{state==='breached'?<rect className="map-node-port" x={r+7} y="-4" width="8" height="8"/>:<circle className="map-node-port" cx={r+11} cy="0" r="4"/>}<text textAnchor="middle" y={r+36}>{graphText(service.display_name,27)}</text><text className="subtitle" textAnchor="middle" y={r+58}>{service.owner&&<>{graphText(service.owner,27)} · </>}{count} SLO{count===1?'':'s'}</text></g>;})}
    </g></svg></div>
   </div>
   <div className="graph-mobile"><h3>Used by</h3><RelationshipList records={selected.used_by??[]} count={id=>records.find(service=>service.id===id)?.slo_summary?.total} summary={id=>records.find(service=>service.id===id)?.slo_summary}/><div className="selected-focus"><strong>{selected.display_name}</strong><p>{selected.description}</p></div><h3>Depends on</h3><RelationshipList records={selected.dependency_services??[]} count={id=>records.find(service=>service.id===id)?.slo_summary?.total} summary={id=>records.find(service=>service.id===id)?.slo_summary}/></div>
   <div className="graph-details"><span className="service-symbol" aria-hidden="true">{selected.display_name[0]}</span><div className="service-caption"><strong>{selected.display_name}</strong><small>{selected.description}</small></div><ServiceSummaryMark summary={selected.slo_summary}/><span className="graph-counter">{selected.used_by_count??0} used by</span><ServiceLink href={`/services/${selected.id}`}>View service</ServiceLink></div>
  </section>:!busy&&!error?<p className="service-muted">Create a service to explore its dependencies.</p>:busy?<span className="sr-only" role="status">Opening dependency graph</span>:null}
  {next&&<div className="service-graph-more"><span>Showing {records.length} of {total} services.</span><Button variant="outline" pending={busy} onClick={()=>load(next)}>Show more services</Button></div>}
 </>;
}
