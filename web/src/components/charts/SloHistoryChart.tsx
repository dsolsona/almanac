import { useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent, type ReactNode } from 'react';
import type { components } from '../../api/generated.ts';
import { traceChartRender } from '../../telemetry/client.ts';
import { percentExact } from '../../slos/detailFormat.ts';
import './slo-history-chart.css';

type Sample=components['schemas']['SloOperationalSample'];
type Props={kind:'budget'|'burn'|'attainment';samples:Sample[];start:string;end:string;target:string;compact?:boolean;zone?:'UTC'|'local';generation?:string;burnLabel?:string};
const labels:Record<Sample['state'],string>={observed:'Complete evaluation',missing:'Missing',partial:'Partial data',stale:'Stale',zero_traffic:'Zero traffic',known_empty:'Known empty',expired:'Expired history',not_applicable:'Not applicable'};
export function pointTime(value:string,zone:'UTC'|'local'='UTC'):string {
 return new Date(value).toLocaleString('en-GB',{day:'2-digit',month:'short',year:'numeric',hour:'2-digit',minute:'2-digit',hour12:false,...(zone==='UTC'?{timeZone:'UTC'}:{timeZoneName:'shortOffset'})})+(zone==='UTC'?' UTC':' local');
}
export function SloHistoryChart({kind,samples,start,end,target,compact=false,zone='UTC',generation,burnLabel='1 h'}:Props) {
 const host=useRef<HTMLDivElement>(null),tooltip=useRef<HTMLDivElement>(null);
 const [width,setWidth]=useState(980),[index,setIndex]=useState<number|null>(null);
 useLayoutEffect(()=>{const el=host.current;if(!el)return;const resize=()=>setWidth(Math.max(280,el.clientWidth-(innerWidth>700?38:14)));resize();const observer=new ResizeObserver(resize);observer.observe(el);return()=>observer.disconnect();},[start,end]);
 useEffect(()=>setIndex(null),[samples,start,end,kind]);
 useEffect(()=>{const close=(e:globalThis.PointerEvent)=>{if(e.target instanceof Node&&!host.current?.contains(e.target))setIndex(null);};const escape=(e:KeyboardEvent)=>{if(e.key==='Escape')setIndex(null);};document.addEventListener('pointerdown',close);document.addEventListener('keydown',escape);return()=>{document.removeEventListener('pointerdown',close);document.removeEventListener('keydown',escape);};},[]);
 const height=compact?160:225,left=65,right=width-20,top=16,bottom=height-28;
 const field=kind==='budget'?'remaining_fraction':kind==='burn'?'burn':'attainment';
 const numeric=(p:Sample)=>p[field]===null?null:Number(p[field])*(kind==='burn'?1:100);
 const allowed=(p:Sample)=>['observed','partial','stale'].includes(p.state)&&numeric(p)!==null;
 const values=samples.filter(allowed).map(p=>numeric(p)!);
 const floor=kind==='budget'?Math.min(-25,...values):kind==='burn'?0:Math.min(99.4,Number(target)*100,...values);
 const ceiling=kind==='burn'?Math.max(6,...values):Math.max(100,...values);
 const ticks=kind==='budget'&&floor===-25?[100,50,0,-25]:Array.from({length:4},(_,i)=>ceiling-i*(ceiling-floor)/3);
 const min=Date.parse(start),max=Date.parse(end),x=(p:Sample)=>left+(Date.parse(p.evaluation_at)-min)/Math.max(1,max-min)*(right-left),y=(v:number)=>top+(ceiling-v)/Math.max(.001,ceiling-floor)*(bottom-top);
 const unit=kind==='burn'?'×':'%',label=kind==='budget'?'Rolling budget remaining':kind==='burn'?`Burn rate · ${burnLabel} window`:'Interval attainment';
 const plotted=useMemo(()=>{
 let nodes:ReactNode=null;
 traceChartRender(()=>{
 const segments:Sample[][]=[];let segment:Sample[]=[];
 for(const p of samples){const qualified=p.state!=='observed'||p.quality?.state!=='complete'||p.quality?.freshness!=='fresh'||!!p.display_coverage?.states.some(state=>state!=='observed')||!!p.display_coverage?.quality_states.some(state=>state!=='complete')||!!p.display_coverage?.freshness.some(state=>state!=='fresh');if(!allowed(p)||qualified){if(segment.length)segments.push(segment);segment=[];if(allowed(p))segments.push([p]);}else segment.push(p);}if(segment.length)segments.push(segment);
 nodes=segments.map((points,i)=><g key={i}>{kind==='budget'&&points.length>1&&<path className="fill" d={`M${x(points[0]!)},${y(0)} ${points.map(p=>`L${x(p)},${y(numeric(p)!)}`).join(' ')} L${x(points.at(-1)!)},${y(0)} Z`}/>}<path className="curve" d={points.map((p,j)=>`${j?'L':'M'}${x(p)},${y(numeric(p)!)}`).join(' ')}/>{points.length===1&&<circle className={`qualified-point ${points[0]!.state}`} cx={x(points[0]!)} cy={y(numeric(points[0]!)!)} r="3"/>}</g>);
 });return nodes;
 },[samples,kind,width,start,end,target,compact,zone,generation]);
 const selected=index===null?undefined:samples[index];
 useLayoutEffect(()=>{if(!selected||!tooltip.current||!host.current)return;const frame=host.current.getBoundingClientRect(),svg=host.current.querySelector('svg')!.getBoundingClientRect(),px=svg.left-frame.left+x(selected)/width*svg.width;tooltip.current.style.left=`${Math.max(8,Math.min(frame.width-tooltip.current.offsetWidth-8,px+14))}px`;});
 function inspect(e:PointerEvent<HTMLDivElement>){const svg=e.currentTarget.querySelector('svg');if(!svg||!samples.length)return;const r=svg.getBoundingClientRect(),value=min+((e.clientX-r.left)/r.width*width-left)/(right-left)*(max-min);let nearest=0;for(let i=1;i<samples.length;i++)if(Math.abs(Date.parse(samples[i]!.evaluation_at)-value)<Math.abs(Date.parse(samples[nearest]!.evaluation_at)-value))nearest=i;setIndex(nearest);}
 const tickFractions=width<480?[0,6/11,1]:[0,4/11,8/11,1];
 const selectedValue=selected&&allowed(selected)?(kind==='burn'?`${selected.burn}×`:percentExact(selected[field])):null;
 if(!Number.isFinite(min)||!Number.isFinite(max)||max<=min)return <p role="alert">Chart unavailable: Timeline range must have end after start</p>;
 return <div ref={host} className={`slo-history-chart ${kind}`} data-chart-ready="true" data-rendered-generation={generation} data-rendered-zone={zone} role="group" aria-label={`Inspect ${label}. Use arrow keys, Home, End, Escape.`} tabIndex={samples.length?0:undefined}
 onPointerMove={e=>{if(e.pointerType!=='touch')inspect(e);}} onPointerDown={e=>{if(e.pointerType==='touch')inspect(e);}} onPointerLeave={e=>{if(e.pointerType!=='touch')setIndex(null);}} onBlur={()=>setIndex(null)}
 onKeyDown={e=>{if(e.key==='Escape'){setIndex(null);return;}if(!samples.length||!['Home','End','ArrowLeft','ArrowRight'].includes(e.key))return;e.preventDefault();setIndex(i=>e.key==='Home'?0:e.key==='End'?samples.length-1:Math.max(0,Math.min(samples.length-1,(i??samples.length-1)+(e.key==='ArrowRight'?1:-1))));}}>
 {samples.length===0?<p className="slo-history-empty">No history available for this period.</p>:<svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${label}; unavailable measurements are gaps.`}>
 {ticks.map((t,i)=><g key={i}><line className="gridline" x1={left} x2={right} y1={y(t)} y2={y(t)}/><text className="axis" x="55" y={y(t)+4} textAnchor="end">{Number(t.toFixed(kind==='attainment'?3:2))}{unit}</text></g>)}
 <line className="ref" x1={left} x2={right} y1={y(kind==='budget'?0:kind==='burn'?1:Number(target)*100)} y2={y(kind==='budget'?0:kind==='burn'?1:Number(target)*100)}/>
 {plotted}
 {tickFractions.map((fraction,i)=>{const t=min+fraction*(max-min);return <text className="axis" key={i} x={left+fraction*(right-left)} y={height-7} textAnchor={i===0?'start':i===tickFractions.length-1?'end':'middle'}>{new Date(t).toLocaleDateString('en-GB',{day:'2-digit',month:'short',...(zone==='UTC'?{timeZone:'UTC'}:{})})}{i===tickFractions.length-1?` ${zone==='UTC'?'UTC':'local'}`:''}</text>;})}
 {selected&&<g><line className="cursor" x1={x(selected)} x2={x(selected)} y1={top} y2={bottom}/>{allowed(selected)&&<circle className="inspection-dot" cx={x(selected)} cy={y(numeric(selected)!)} r="4.5"/>}</g>}
 </svg>}
 {selected&&<div ref={tooltip} className="slo-point-tooltip" role="tooltip"><span>{pointTime(selected.evaluation_at,zone)}</span><strong>{selectedValue??labels[selected.state]}</strong><span>{label}</span><small>{selected.state==='observed'&&(selected.quality?.state!=='complete'||selected.quality?.freshness!=='fresh')?`Observed · ${selected.quality?.state.replaceAll('_',' ')} · ${selected.quality?.freshness}`:labels[selected.state]}</small>{selected.display_coverage&&<small>Displayed period includes {[...new Set([...selected.display_coverage.states,...selected.display_coverage.quality_states,...selected.display_coverage.freshness])].join(', ').replaceAll('_',' ')}</small>}</div>}
 <span className="sr-only" aria-live="polite">{selected?`${pointTime(selected.evaluation_at,zone)} · ${selectedValue??labels[selected.state]}`:''}</span>
 </div>;
}
