import type {MarkTone} from '../components/ui/index.ts';
import type {Service,Summary,ServiceWrite,OverviewItem,ServiceLink} from './client.ts';
export const suggestName=(value:string)=>value.normalize('NFKD').replace(/\p{M}/gu,'').toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-+|-+$/g,'').slice(0,63).replace(/-+$/g,'');
export const namePattern=/^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/;
export const labelKeyPattern=/^[A-Za-z0-9]([-._A-Za-z0-9]{0,61}[A-Za-z0-9])?$/;
export function labelPairs(labels:Service['labels']):[string,string][]{return Object.entries(labels??{}).flatMap(([key,values])=>values.map(value=>[key,value] as [string,string]));}
export function labelsObject(pairs:readonly (readonly [string,string])[]):Record<string,string[]>{const labels:Record<string,string[]>={};for(const [key,value]of pairs){if(!Object.hasOwn(labels,key))Object.defineProperty(labels,key,{value:[],writable:true,enumerable:true,configurable:true});labels[key]!.push(value);}return labels;}
export function summaryMark(summary:Summary):{tone:MarkTone;label:string}{return summary.breached>0?{tone:'breached',label:'Breached'}:summary.unknown>0?{tone:'stale',label:'Unknown'}:summary.total>0?{tone:'healthy',label:'On track'}:{tone:'none',label:'No SLOs'};}
export function objectiveMark(item:OverviewItem):{tone:MarkTone;label:string}{
 if(item.current_state==='stale'||item.current_state==='failed'||item.current_state==='unavailable'||item.current.quality.freshness!=='fresh')return {tone:'stale',label:'Stale data'};
 if(item.current.quality.state!=='complete'||item.current.performance.provisional)return {tone:'stale',label:item.current.quality.state==='partial'?'Partial data':'Unknown'};
 if(item.current.performance.state==='violated')return {tone:'breached',label:'Breached'};
 return item.current.performance.state==='met'?{tone:'healthy',label:'On track'}:{tone:'stale',label:'Unknown'};
}
export function percent(value:string|null):string{
 if(value===null)return '—';
 const negative=value.startsWith('-'),raw=negative?value.slice(1):value,[whole,fraction='']=raw.split('.'),digits=whole+fraction,point=whole.length+2;
 let shifted=point>=digits.length?digits+'0'.repeat(point-digits.length):digits.slice(0,point)+'.'+digits.slice(point);
 shifted=shifted.replace(/^0+(?=\d)/,'').replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');
 return `${negative?'−':''}${shifted}%`;
}
export const windowLabel=(seconds:number)=>seconds%86400===0?`${seconds/86400} d`:seconds%3600===0?`${seconds/3600} h`:`${seconds} s`;
export function validLink(link:ServiceLink):boolean{try{const url=new URL(link.url);return !!link.name.trim()&&link.name.length<=80&&link.url.length<=2048&&['http:','https:'].includes(url.protocol)&&!url.username&&!url.password&&!/[\s\u0000-\u001f]/.test(link.url);}catch{return false;}}
export function validateService(body:ServiceWrite):Record<string,string>{
 const errors:Record<string,string>={};
 if(!body.display_name.trim()||[...body.display_name].length>128)errors.display_name='Enter a display name of up to 128 characters.';
 if(!namePattern.test(body.name))errors.name='Use lowercase letters, numbers and hyphens, up to 63 characters.';
 if((body.description??'').length>4000)errors.description='Use up to 4,000 characters.';
 if((body.owner??'').length>200)errors.owner='Use up to 200 characters.';
 if((body.contact??'').length>320)errors.contact='Use up to 320 characters.';
 const pairs=labelPairs(body.labels);if(pairs.length>100||pairs.some(([key,value])=>!labelKeyPattern.test(key)||!value.trim()||[...value].length>256))errors.labels='Use up to 100 key:value labels with values up to 256 characters.';
 if((body.links??[]).length>50||(body.links??[]).some(link=>!validLink(link)))errors.links='Use named http:// or https:// links without credentials.';
 if((body.dependencies??[]).length>100)errors.dependencies='Choose up to 100 dependencies.';
 return errors;
}
