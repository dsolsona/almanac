import { useEffect, useId, useLayoutEffect, useRef, useState, type PointerEvent } from 'react';
import { StatusMark } from '../ui/index.ts';
import { countText, measurementBad, measurementLabels, type MeasurementInterval } from '../../slis/measurements.ts';
import './sli-measurements-chart.css';

export function intervalClock(value:string):string { return new Date(value).toISOString().slice(11,19).replace(/:00$/,''); }
export function intervalTime(row:Pick<MeasurementInterval,'start'|'end'>):string { return `${intervalClock(row.start)}–${intervalClock(row.end)} UTC`; }
export function SliMeasurementsChart({ intervals, bucketed = false, requestedStart }: { intervals:MeasurementInterval[]; bucketed?:boolean; requestedStart?:number }) {
  const [mobile,setMobile]=useState(()=>window.matchMedia?.('(max-width:700px)').matches??false);
  const [index,setIndex]=useState<number|null>(null);
  const frame=useRef<HTMLDivElement>(null), tooltip=useRef<HTMLDivElement>(null), pointerY=useRef<number|undefined>(undefined);
  const labelID=useId();
  useEffect(()=>{ const media=window.matchMedia?.('(max-width:700px)'); if(!media)return; const change=()=>{setMobile(media.matches);setIndex(null);}; media.addEventListener('change',change);return()=>media.removeEventListener('change',change); },[]);
  useEffect(()=>{setIndex(null);},[intervals]);
  useEffect(()=>{ const outside=(event:globalThis.PointerEvent)=>{ if(event.target instanceof Node&&!frame.current?.contains(event.target))setIndex(null); };const escape=(event:KeyboardEvent)=>{if(event.key==='Escape')setIndex(null);};document.addEventListener('pointerdown',outside);document.addEventListener('keydown',escape);return()=>{document.removeEventListener('pointerdown',outside);document.removeEventListener('keydown',escape);}; },[]);
  const width=mobile?320:980,left=mobile?44:48,right=width-(mobile?8:30),top=18,bottom=222;
  const minimum=Math.min(92,...intervals.flatMap(row=>row.ratio===null?[]:[Number(row.ratio)]));
  const floor=Math.max(0,Math.floor(minimum/2)*2), span=100-floor;
  const first = Date.parse(intervals[0]?.start ?? '');
  const last = Date.parse(intervals.at(-1)?.end ?? '');
  const rangeStart = requestedStart !== undefined && requestedStart < first ? requestedStart : first;
  const showEarlier = rangeStart < first;
  const timeX = (time: number) => left + (time - rangeStart) * (right - left) / (last - rangeStart);
  const x=(i:number)=>showEarlier
    ? timeX((Date.parse(intervals[i]!.start) + Date.parse(intervals[i]!.end)) / 2)
    : left+i*(right-left)/Math.max(1,intervals.length-1);
  const y=(value:number)=>top+(100-value)/span*(bottom-top);
  const cellWidth=showEarlier ? (right-timeX(first))/intervals.length : (right-left)/Math.max(1,intervals.length-1);
  const gapWidth=bucketed?cellWidth:Math.max(2,(right-left)/Math.max(1,intervals.length)-2);
  const markWidth=Math.min(12,bucketed?cellWidth*.8:gapWidth);
  const segments:{x:number;y:number}[][]=[];
  let segment:{x:number;y:number}[]=[];
  intervals.forEach((row,i)=>{if(row.ratio===null){if(segment.length)segments.push(segment);segment=[];}else segment.push({x:x(i),y:y(Number(row.ratio))});});
  if(segment.length)segments.push(segment);
  const selected=index===null?undefined:intervals[index];
  useLayoutEffect(()=>{
    const host=frame.current,panel=tooltip.current,svg=host?.querySelector('svg');
    if(!host||!panel||!svg||!selected||index===null)return;
    const bounds=host.getBoundingClientRect(),plot=svg.getBoundingClientRect(),scale=plot.width/width;
    const screenX=plot.left+x(index)*scale,screenY=pointerY.current??plot.top+y(Number(selected.ratio??100))*scale;
    panel.style.left=`${Math.max(0,Math.min(screenX-bounds.left+16,bounds.width-panel.offsetWidth))}px`;
    panel.style.top=`${Math.max(8-bounds.top,screenY+16+panel.offsetHeight<=innerHeight-8?screenY-bounds.top+16:screenY-bounds.top-panel.offsetHeight-16)}px`;
  });
  function inspect(event:PointerEvent<HTMLDivElement>) {
    if((event.target as Element).closest('.sli-chart-tooltip'))return;
    const direct=(event.target as Element).closest('[data-interval]')?.getAttribute('data-interval');
    const svg=event.currentTarget.querySelector('svg')!,bounds=svg.getBoundingClientRect();
    const position=bounds.width?(event.clientX-bounds.left)*width/bounds.width:left;
    if(direct===undefined&&(position<left-8||position>right+8))return;
    pointerY.current=event.clientY;
    if (direct === undefined && showEarlier) {
      const at = rangeStart + (position-left) * (last-rangeStart) / (right-left);
      const index = intervals.findIndex(row => at < Date.parse(row.end));
      setIndex(at < first ? null : index === -1 ? intervals.length - 1 : index);
      return;
    }
    setIndex(direct!==undefined?Number(direct):Math.max(0,Math.min(intervals.length-1,Math.round((position-left)/(right-left)*(intervals.length-1)))));
  }
  if(!intervals.length)return <p className="sli-chart-help">No inspected intervals are available to plot.</p>;
  const dateLabel = (value: string) => new Date(value).toLocaleDateString('en-GB', {day: 'numeric', month: 'short', timeZone: 'UTC'});
  const longRange = bucketed && last - rangeStart > 24 * 3600_000;
  const intervalLabel = (row: MeasurementInterval) => bucketed && row.start.slice(0, 10) !== row.end.slice(0, 10)
    ? `${dateLabel(row.start)} ${intervalClock(row.start)}–${dateLabel(row.end)} ${intervalClock(row.end)} UTC`
    : `${dateLabel(row.start)} · ${intervalTime(row)}`;
  const minutes=!bucketed&&Date.parse(intervals[0]!.end)-Date.parse(intervals[0]!.start)===60_000;
  const ticks=mobile?[0,Math.floor(intervals.length/2),intervals.length-1]:[0,1,2,3,4].map(i=>Math.min(intervals.length-1,Math.round(i*intervals.length/4)));
  return <>
    <div ref={frame} className="sli-inspectable-chart" role="group" tabIndex={0} aria-label="Interactive measurements. Hover or tap to inspect. Use left and right arrow keys between intervals, Home and End for first and last, Escape to dismiss." aria-describedby={labelID}
      onFocus={()=>{pointerY.current=undefined;setIndex(value=>value??intervals.length-1);}} onBlur={()=>setIndex(null)}
      onPointerMove={event=>{if(event.pointerType!=='touch')inspect(event);}} onPointerDown={event=>{if(event.pointerType==='touch')inspect(event);}} onPointerLeave={event=>{if(event.pointerType!=='touch')setIndex(null);}}
      onKeyDown={event=>{if(event.key==='Escape'){setIndex(null);return;}if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();pointerY.current=undefined;setIndex(current=>event.key==='Home'?0:event.key==='End'?intervals.length-1:Math.max(0,Math.min(intervals.length-1,(current??intervals.length-1)+(event.key==='ArrowRight'?1:-1))));}}>
      <svg className="sli-measurement-svg" viewBox={`0 0 ${width} 312`} role="img" aria-label="Good-to-total ratio for each inspected interval. Unavailable ratios appear as gaps. Exact counts are available by inspection or in Recent intervals.">
        {showEarlier && <rect className="outside-history" x={left} y={top} width={timeX(first)-left} height={bottom-top}/>}
        {[0,1,2,3,4].map(i=>{const value=100-i*span/4;return <g key={i}><line className="grid" x1={left} x2={right} y1={y(value)} y2={y(value)}/><text x="35" y={y(value)+4} textAnchor="end">{value}%</text></g>;})}
        {intervals.map((row,i)=>{
          if(row.ratio!==null)return null;
          const start=showEarlier?timeX(Date.parse(row.start)):intervals.length===1?left:Math.max(left,x(i)-gapWidth/2);
          const end=showEarlier?timeX(Date.parse(row.end)):intervals.length===1?right:Math.min(right,x(i)+gapWidth/2);
          return <rect key={row.start} className="gap" x={start} y={top} width={end-start} height={bottom-top}/>;
        })}
        {segments.filter(points=>points.length>1).map((points,i)=><g key={i}><path className="area" d={`M${points[0]!.x},${bottom} ${points.map(p=>`L${p.x},${p.y}`).join(' ')} L${points.at(-1)!.x},${bottom} Z`}/><path className="line" d={points.map((p,j)=>`${j?'L':'M'}${p.x},${p.y}`).join(' ')}/></g>)}
        {intervals.map((row,i)=>row.ratio===null||intervals[i-1]?.ratio!=null||intervals[i+1]?.ratio!=null?null:<circle key={row.start} className={`chart-point ${row.state}`} data-interval={i} data-point={i} cx={x(i)} cy={y(Number(row.ratio))} r={bucketed?Math.min(2.5,Math.max(showEarlier?1.5:0,cellWidth*.4)):2.5}/>)}
        <g className="interval-states"><text x={left} y={bottom+22}>{`Collection · one square${mobile?' / ':' per '}${bucketed?'bucket':minutes?(mobile?'min':'minute'):'interval'}`}</text>{intervals.map((row,i)=><rect key={row.start} className={`interval-state ${row.state}`} data-interval={i} x={x(i)-markWidth/2} y={bottom+31} width={markWidth} height={markWidth}/>)}</g>
        {selected&&index!==null&&<g className="inspection-cursor"><line className="inspection-line" x1={x(index)} x2={x(index)} y1={top} y2={bottom+43}/>{selected.ratio!==null&&<circle className={`active-dot ${selected.state}`} cx={x(index)} cy={y(Number(selected.ratio))} r="5"/>}</g>}
        {showEarlier ? (mobile ? [0,.5,1] : [0,.25,.5,.75,1]).map(fraction => {
          const value = new Date(rangeStart + fraction * (last - rangeStart)).toISOString();
          const label = longRange ? dateLabel(value) : intervalClock(value);
          return <text key={fraction} x={left + fraction * (right - left)} y="297" textAnchor={fraction === 0 ? 'start' : fraction === 1 ? 'end' : 'middle'}>{label}{fraction === 1 && !longRange ? ' UTC' : ''}</text>;
        }) : [...new Set(ticks)].map((i,n)=><text key={i} x={x(i)} y="297" textAnchor={n===0?'start':i===intervals.length-1?'end':'middle'}>{longRange?dateLabel(i===intervals.length-1?intervals[i]!.end:intervals[i]!.start):i===intervals.length-1?`${intervalClock(intervals[i]!.end)} UTC`:intervalClock(intervals[i]!.start)}</text>)}
      </svg>
      {selected&&<div ref={tooltip} className="sli-chart-tooltip" role="tooltip"><p className="small">{bucketed?intervalLabel(selected):`${new Date(selected.start).toLocaleDateString('en-GB',{day:'2-digit',month:'short',year:'numeric',timeZone:'UTC'})} · ${intervalTime(selected)}`}</p><p className="tooltip-result">{selected.ratio===null?'No ratio available':`${selected.ratio}%`}</p><StatusMark tone={selected.state==='observed'?'healthy':selected.state==='failed'||selected.state==='partial'?'warning':'none'} label={measurementLabels[selected.state]}/>{bucketed&&<p className="tooltip-reason">{selected.detail}</p>}{selected.good!==null&&selected.total!==null?<dl><div><dt>Good</dt><dd>{countText(selected.good)}</dd></div><div><dt>Total</dt><dd>{countText(selected.total)}</dd></div><div><dt>Unsuccessful</dt><dd>{countText(measurementBad(selected.good,selected.total))}</dd></div></dl>:!bucketed&&<p className="tooltip-reason">{selected.detail}. Counts are unknown.</p>}</div>}
      <span id={labelID} className="sr-only" aria-live="polite">{selected?`${bucketed?intervalLabel(selected):intervalTime(selected)}. ${measurementLabels[selected.state]}. ${bucketed?selected.detail+'. ':''}${selected.ratio===null?(bucketed?'No ratio available':selected.detail):`${selected.ratio} percent. ${selected.good} good out of ${selected.total}.`}`:''}</span>
    </div>
    {showEarlier && <p className="sli-chart-help">Shaded area is outside available measurement history. Available history starts {dateLabel(intervals[0]!.start)} at {intervalClock(intervals[0]!.start)} UTC.</p>}
    <p className="sli-chart-help">{`Hover or tap to inspect · keyboard: ← → between ${bucketed?'buckets':minutes?'minutes':'intervals'}`}</p>
  </>;
}
