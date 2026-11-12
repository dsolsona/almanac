// Decimal presentation only; calculations and classifications remain server-owned.
export function exactDecimal(value:string|null|undefined):string {return value==null?'—':value.replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');}
export function percentExact(value:string|null|undefined):string {
 if(value==null)return '—';const negative=value.startsWith('-'),raw=negative?value.slice(1):value,[whole,frac='']=raw.split('.'),digits=whole+frac,point=whole!.length+2;
 const shifted=(point>=digits.length?digits+'0'.repeat(point-digits.length):digits.slice(0,point)+'.'+digits.slice(point)).replace(/^0+(?=\d)/,'');return `${negative?'-':''}${exactDecimal(shifted)}%`;
}
export function countExact(value:string|null|undefined):string {const raw=exactDecimal(value);const [whole,fraction]=raw.split('.');return whole!.replace(/\B(?=(\d{3})+(?!\d))/g,',')+(fraction?'.'+fraction:'');}
export function durationText(seconds:number):string {return seconds%86400===0?`${seconds/86400} day${seconds===86400?'':'s'}`:seconds%3600===0?`${seconds/3600} hour${seconds===3600?'':'s'}`:`${seconds} second${seconds===1?'':'s'}`;}

export function burnText(value:string|null):string {return value===null?'—':`${Number(value).toLocaleString('en-US',{minimumFractionDigits:2,maximumFractionDigits:2})}×`;}
export function errorRateText(target:string):string {
 const [whole,fraction='']=target.split('.'),scale=10n**BigInt(fraction.length),remaining=scale-BigInt(whole!+fraction);
 const digits=remaining.toString().padStart(fraction.length+1,'0');
 return percentExact(fraction.length?`${digits.slice(0,-fraction.length)}.${digits.slice(-fraction.length)}`:digits);
}
