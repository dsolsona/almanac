import type { SliObservation } from './client.ts';

export type MeasurementState = 'observed' | 'failed' | 'missing' | 'partial' | 'empty' | 'zero';
export type MeasurementInterval = { start:string; end:string; state:MeasurementState; good:string|null; total:string|null; ratio:string|null; detail:string };
const scale = 1_000_000_000_000n;
function scaled(value:string):bigint { const [whole, fraction=''] = value.split('.'); return BigInt(whole!) * scale + BigInt(fraction.padEnd(12,'0')); }
function decimal(value:bigint):string { const fraction = (value % scale).toString().padStart(12,'0').replace(/0+$/,''); return `${value / scale}${fraction ? `.${fraction}` : ''}`; }
export function countText(value:string):string { const [whole,fraction] = value.split('.'); return `${whole!.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}${fraction ? `.${fraction}` : ''}`; }
export function measurementRatio(good:string|null,total:string|null):string|null {
  if (good === null || total === null || scaled(total) === 0n) return null;
  const denominator=scaled(total), hundredths=(scaled(good)*10_000n+denominator/2n)/denominator;
  return `${hundredths/100n}.${(hundredths%100n).toString().padStart(2,'0')}`;
}
export function measurementBad(good:string,total:string):string { return decimal(scaled(total)-scaled(good)); }
export const measurementLabels:Record<MeasurementState,string> = { observed:'Observed', failed:'Query error', missing:'Missing', partial:'Partial data', empty:'Known empty', zero:'Zero traffic' };
export function measurementInterval(row:SliObservation):MeasurementInterval {
  const state:MeasurementState = row.outcome === 'failed' ? 'failed' : row.outcome === 'incomplete' ? 'partial' : row.outcome === 'complete_empty' ? 'empty' : row.total_count !== null && scaled(row.total_count) === 0n ? 'zero' : 'observed';
  const detail = state === 'failed' ? row.error_code === 'timeout' || row.error_code === 'deadline' ? 'Query timed out' : `Query failed: ${row.error_code}` : state === 'partial' ? `Missing ${row.missing_components.join(' and ')} count` : state === 'empty' ? 'Queries returned no count values' : state === 'zero' ? 'No events were recorded' : '';
  return { start:row.interval_start, end:row.interval_end, state, good:row.good_count, total:row.total_count, ratio:measurementRatio(row.good_count,row.total_count), detail };
}
export function buildMeasurements(rows:SliObservation[],start:string|null,end:string|null,cadenceSeconds:number) {
  const intervals:MeasurementInterval[] = [], counts:Record<MeasurementState,number> = {observed:0,failed:0,missing:0,partial:0,empty:0,zero:0};
  let good=0n,total=0n,minimum:MeasurementInterval|null=null;
  const lookup = new Map(rows.map(row => [Date.parse(row.interval_start),row]));
  const from=Date.parse(start??''), to=Date.parse(end??''), step=cadenceSeconds*1000;
  if (Number.isFinite(from) && Number.isFinite(to) && Number.isFinite(step) && step>0) {
    for (let at=from;at+step<=to;at+=step) {
      const row=lookup.get(at);
      const item:MeasurementInterval = row && Date.parse(row.interval_end) === at+step ? measurementInterval(row) : { start:new Date(at).toISOString(), end:new Date(at+step).toISOString(), state:'missing', good:null,total:null,ratio:null,detail:'No observation received' };
      intervals.push(item); counts[item.state]++;
      if (item.good!==null && item.total!==null) { good+=scaled(item.good); total+=scaled(item.total); }
      if (item.ratio!==null && (!minimum || scaled(item.good!)*scaled(minimum.total!) < scaled(minimum.good!)*scaled(item.total!))) minimum=item;
    }
  }
  return { intervals, counts, good:decimal(good), total:decimal(total), bad:decimal(total-good), ratio:measurementRatio(decimal(good),decimal(total)), minimum };
}
