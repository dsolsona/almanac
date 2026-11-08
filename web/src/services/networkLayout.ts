import type {Service} from './client.ts';
export type Point=readonly [number,number];
type Group={owner:string;cx:number;cy:number;rx:number;ry:number};
type Layout={positions:Record<string,Point>;width:number;height:number;ordered:Service[];groups:Group[];labelScales:Record<string,number>};

export function networkLayout(records:Service[],grouped=false):Layout{
 const ordered=[...records].sort((a,b)=>a.display_name.localeCompare(b.display_name)||a.id.localeCompare(b.id));
 if(!ordered.length)return {positions:{},width:1040,height:600,ordered,groups:[],labelScales:{}};
 if(grouped){
  const owners=[...new Set(ordered.map(service=>service.owner??''))].sort((a,b)=>a.localeCompare(b));
  const clusters=owners.map(owner=>{const layout=networkLayout(ordered.filter(service=>(service.owner??'')===owner));return {owner,layout,width:Math.max(340,layout.width*1.2),height:Math.max(300,layout.height*1.2+36)};});
  const pack=(columns:number)=>{let x=24,y=24,rowHeight=0,width=0;
   const placed=clusters.map((group,index)=>{if(index>0&&index%columns===0){x=24;y+=rowHeight+28;rowHeight=0;}const placement={...group,left:x+(group.width-group.layout.width)/2,top:y+(group.height-group.layout.height)/2+12,cx:x+group.width/2,cy:y+group.height/2};x+=group.width+28;rowHeight=Math.max(rowHeight,group.height);width=Math.max(width,x);return placement;});
   return {placed,width:width+24,height:y+rowHeight+24};
  };
  const packed=Array.from({length:clusters.length},(_,index)=>pack(index+1)).sort((a,b)=>Math.max(a.width/1.7,a.height)-Math.max(b.width/1.7,b.height))[0]!,{placed}=packed;
  const positions=Object.fromEntries(placed.flatMap(group=>Object.entries(group.layout.positions).map(([id,[px,py]])=>[id,[px+group.left,py+group.top] as const])));
  return {positions,width:packed.width,height:packed.height,ordered,labelScales:Object.assign({},...placed.map(group=>group.layout.labelScales)),groups:placed.map(group=>({owner:group.owner,cx:group.cx,cy:group.cy,rx:group.width/2-8,ry:group.height/2-8}))};
 }
 const nodes=ordered.map((service,index)=>{const angle=index*Math.PI*(3-Math.sqrt(5)),radius=95*Math.sqrt(index+.5),label=[...service.display_name].slice(0,27).reduce((sum,char)=>sum+((char.codePointAt(0)??0)>255||/[MW@]/.test(char)?20:/[A-Z]/.test(char)?15:char===' '?6:11),0);return {service,index,x:Math.cos(angle)*radius*1.35,y:Math.sin(angle)*radius*.85,vx:0,vy:0,fx:0,fy:0,width:Math.max(128,label+24)};});
 const byID=new Map(nodes.map(node=>[node.service.id,node]));
 const links=nodes.flatMap(from=>(from.service.dependencies??[]).flatMap(id=>{const to=byID.get(id);return to&&to!==from?[{from,to}]:[];})).sort((a,b)=>a.from.index-b.from.index||a.to.index-b.to.index);
 const separate=()=>{let overlap=false;for(let i=0;i<nodes.length;i++)for(let j=i+1;j<nodes.length;j++){
  const a=nodes[i]!,b=nodes[j]!,dx=b.x-a.x,dy=b.y-a.y,ox=(a.width+b.width)/2+20-Math.abs(dx),oy=168-Math.abs(dy);
  if(ox<=0||oy<=0)continue;overlap=true;
  if(ox<oy){const shift=(ox+.1)/2*(dx<0?-1:1);a.x-=shift;b.x+=shift;}else{const shift=(oy+.1)/2*(dy<0?-1:1);a.y-=shift;b.y+=shift;}
 }return overlap;};
 // Solve once with a fixed seed/order and iteration budget; selection never restarts the layout.
 for(let step=0;step<180;step++){
  const heat=1-step/210;
  for(const node of nodes){node.fx=-node.x*.003;node.fy=-node.y*.007;}
  for(let i=0;i<nodes.length;i++)for(let j=i+1;j<nodes.length;j++){
   const a=nodes[i]!,b=nodes[j]!,dx=b.x-a.x,dy=b.y-a.y,distance=Math.max(1,Math.hypot(dx,dy)),force=8000/(distance*distance),fx=dx/distance*force,fy=dy/distance*force;a.fx-=fx;a.fy-=fy;b.fx+=fx;b.fy+=fy;
  }
  for(const {from,to}of links){const dx=to.x-from.x,dy=to.y-from.y,distance=Math.max(1,Math.hypot(dx,dy)),force=(distance-Math.max(190,(from.width+to.width)/2+36))*.025,fx=dx/distance*force,fy=dy/distance*force;from.fx+=fx;from.fy+=fy;to.fx-=fx;to.fy-=fy;}
  for(const node of nodes){node.vx=(node.vx+node.fx)*.65;node.vy=(node.vy+node.fy)*.65;node.x+=Math.max(-24,Math.min(24,node.vx))*heat;node.y+=Math.max(-24,Math.min(24,node.vy))*heat;}
  separate();
 }
 for(let step=0;step<160&&separate();step++);
 const left=Math.min(...nodes.map(node=>node.x-node.width/2))-36,top=Math.min(...nodes.map(node=>node.y))-100;
 const width=Math.max(...nodes.map(node=>node.x+node.width/2))-left+36,height=Math.max(...nodes.map(node=>node.y))-top+120;
 const stretch=Math.max(1,Math.min(2,1.7*height/width)),labelScale=Math.min(stretch,nodes.length<20?1.6:2);
 const positions=Object.fromEntries(nodes.map(node=>[node.service.id,[Math.round((node.x-left)*stretch*100)/100,Math.round((node.y-top)*100)/100] as const]));
 return {positions,width:width*stretch,height,ordered,groups:[],labelScales:Object.fromEntries(nodes.map(node=>[node.service.id,labelScale]))};
}
