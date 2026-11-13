// Shared exact-point inspection semantics for timeline and SLO history charts.
export function pointTime(value:string,zone:'UTC'|'local'='UTC',exact=false):string {
 const formatted = new Date(value).toLocaleString('en-GB',{day:'2-digit',month:'short',year:'numeric',hour:'2-digit',minute:'2-digit',...(exact?{second:'2-digit' as const}:{}),hour12:false,...(zone==='UTC'?{timeZone:'UTC'}:{timeZoneName:'shortOffset'})})+(zone==='UTC'?' UTC':' local');
 const fraction = exact ? value.match(/\.(\d+)Z$/)?.[1] : undefined;
 return fraction ? formatted.replace(/(\d\d:\d\d:\d\d)/,`$1.${fraction}`) : formatted;
}
export function inspectionIndex(key:string,index:number|null,length:number):number|null {
 if(key==='Escape'||length===0)return null;
 if(key==='Home')return 0;
 if(key==='End')return length-1;
 return Math.max(0,Math.min(length-1,(index??length-1)+(key==='ArrowRight'?1:-1)));
}
