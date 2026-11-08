import {useCallback,useEffect,useLayoutEffect,useRef,useState,type PointerEvent,type KeyboardEvent,type MouseEvent} from 'react';

type Bounds={x:number;y:number;width:number;height:number};
type Size={width:number;height:number};
type Camera={x:number;y:number;scale:number};
function fitCamera(bounds:Bounds,size:Size):Camera{
 const scale=Math.min((size.width-48)/Math.max(1,bounds.width),(size.height-48)/Math.max(1,bounds.height),1);
 return {x:size.width/2-(bounds.x+bounds.width/2)*scale,y:size.height/2-(bounds.y+bounds.height/2)*scale,scale};
}

export function useGraphCamera(world:{width:number;height:number}|null){
 const ref=useRef<SVGSVGElement>(null),[size,setSize]=useState<Size>({width:1040,height:600}),[camera,setCamera]=useState<Camera>({x:0,y:0,scale:1}),[dragging,setDragging]=useState(false);
 const drag=useRef<{id:number;clientX:number;clientY:number;camera:Camera;moved:boolean}|null>(null),ignoreClick=useRef(false);
 const all={x:0,y:0,width:world?.width??1040,height:world?.height??600},minScale=Math.min(.1,fitCamera(all,size).scale/2);
 const fit=useCallback((bounds:Bounds={x:0,y:0,width:world?.width??1040,height:world?.height??600})=>setCamera(fitCamera(bounds,size)),[world,size]);
 useLayoutEffect(()=>{
  const node=ref.current;if(!node)return;
  const measure=()=>{const {width,height}=node.getBoundingClientRect();if(width>48&&height>48)setSize(old=>old.width===width&&old.height===height?old:{width,height});};
  measure();const observer=typeof ResizeObserver==='undefined'?null:new ResizeObserver(measure);observer?.observe(node);return()=>observer?.disconnect();
 },[world]);
 useLayoutEffect(()=>{fit();},[fit]);
 const zoom=useCallback((factor:number,point={x:size.width/2,y:size.height/2})=>{
  setCamera(old=>{const scale=Math.max(minScale,Math.min(4,old.scale*factor)),ratio=scale/old.scale;return {scale,x:point.x-(point.x-old.x)*ratio,y:point.y-(point.y-old.y)*ratio};});
 },[size,minScale]);
 useEffect(()=>{
  const node=ref.current;if(!node)return;
  // React wheel handlers are passive; the canvas must consume its own zoom gesture.
  const wheel=(event:WheelEvent)=>{event.preventDefault();const bounds=node.getBoundingClientRect(),delta=event.deltaY*(event.deltaMode===1?16:event.deltaMode===2?bounds.height:1);zoom(Math.exp(-Math.max(-240,Math.min(240,delta))*.002),{x:event.clientX-bounds.left,y:event.clientY-bounds.top});};
  node.addEventListener('wheel',wheel,{passive:false});return()=>node.removeEventListener('wheel',wheel);
 },[world,zoom]);
 function onPointerDown(event:PointerEvent<SVGSVGElement>){if(event.button!==0||!event.isPrimary)return;ignoreClick.current=false;drag.current={id:event.pointerId,clientX:event.clientX,clientY:event.clientY,camera,moved:false};}
 function onPointerMove(event:PointerEvent<SVGSVGElement>){
  const start=drag.current;if(!start||start.id!==event.pointerId)return;
  if(!(event.buttons&1)){endDrag(event);return;}
  const dx=event.clientX-start.clientX,dy=event.clientY-start.clientY;if(!start.moved&&Math.hypot(dx,dy)<4)return;
  start.moved=true;ignoreClick.current=true;event.currentTarget.setPointerCapture(event.pointerId);setDragging(true);setCamera({...start.camera,x:start.camera.x+dx,y:start.camera.y+dy});
 }
 function endDrag(event:PointerEvent<SVGSVGElement>){if(drag.current?.id!==event.pointerId)return;drag.current=null;setDragging(false);if(event.currentTarget.hasPointerCapture(event.pointerId))event.currentTarget.releasePointerCapture(event.pointerId);}
 function onPointerLeave(event:PointerEvent<SVGSVGElement>){if(!drag.current?.moved)endDrag(event);}
 function onClickCapture(event:MouseEvent<SVGSVGElement>){if(ignoreClick.current&&event.detail>0){ignoreClick.current=false;event.preventDefault();event.stopPropagation();}}
 function onKeyDown(event:KeyboardEvent<SVGSVGElement>){
  if(event.target!==event.currentTarget)return;
  const delta:{[key:string]:[number,number]}={ArrowLeft:[48,0],ArrowRight:[-48,0],ArrowUp:[0,48],ArrowDown:[0,-48]};
  if(delta[event.key]){event.preventDefault();const [x,y]=delta[event.key]!;setCamera(old=>({...old,x:old.x+x,y:old.y+y}));}
  else if(['+','=','-','0'].includes(event.key)){event.preventDefault();if(event.key==='0')fit();else zoom(event.key==='-'?1/1.25:1.25);}
 }
 function reveal([x,y]:readonly [number,number]){if(drag.current)return;const px=camera.x+x*camera.scale,py=camera.y+y*camera.scale;if(px<100||px>size.width-100||py<100||py>size.height-100)setCamera(old=>({...old,x:size.width/2-x*old.scale,y:size.height/2-y*old.scale}));}
 return {ref,size,scale:camera.scale,transform:`translate(${camera.x} ${camera.y}) scale(${camera.scale})`,dragging,fit,zoom,reveal,canZoomIn:camera.scale<4,canZoomOut:camera.scale>minScale,
  events:{onPointerDown,onPointerMove,onPointerLeave,onPointerUp:endDrag,onPointerCancel:endDrag,onLostPointerCapture:endDrag,onClickCapture,onKeyDown}};
}
