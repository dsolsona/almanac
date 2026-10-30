import { Button } from '../ui/index.ts';
import type { ExactValueRow } from './ExactValuesTable.tsx';

export type SloExactRow=ExactValueRow&{good?:string|null;total?:string|null;samples?:number;effectiveAt?:string;window?:string;day?:string};
function percentage(value:string):string {
  const negative=value.startsWith('-'),[whole,fraction='']=(negative?value.slice(1):value).split('.');
  const digits=whole+fraction,point=whole.length+2;
  const shifted=(point>=digits.length?digits+'0'.repeat(point-digits.length):digits.slice(0,point)+'.'+digits.slice(point)).replace(/^0+(?=\d)/,'').replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');
  return `${negative?'-':''}${shifted}%`;
}
function value(row:SloExactRow):string {
  if(row.value===null)return '—';
  return row.lane==='Burn rate'?`${row.value}×`:row.unit==='%'?`${row.value}%`:percentage(row.value);
}

// Table composition copied from slo-tracker's TimelineTable/PagedTable. The row
// adapter retains Almanac's exact decimal strings and distinct coverage states.
export function SloExactValuesTable({rows,title,page,totalRows,onPageChange}:{rows:SloExactRow[];title:string;page:number;totalRows:number;onPageChange:(page:number)=>void}) {
  const daily=title==='Compliance',pages=Math.max(1,Math.ceil(totalRows/50));
  return <div className="app-timeline-table" role="region" aria-label="Exact timeline values"><div className="app-timeline-table-scroll"><table><caption>{title} exact values</caption><thead><tr>{[daily?'Day (UTC)':'Time (UTC)','Value','Good','Total','Samples','Coverage',...(daily?['Evaluated at (UTC)','Window']:[])].map(label=><th scope="col" key={label}>{label}</th>)}</tr></thead><tbody>{rows.map((row,index)=><tr key={`${row.timestamp}-${index}`}><td><time dateTime={daily?row.day:row.timestamp}>{daily?row.day??row.timestamp.slice(0,10):row.timestamp}</time></td><td title={row.details??`${row.value??'Unavailable'} ${row.unit}`}>{daily?`${row.series} · ${value(row)}`:value(row)}</td><td>{row.good??'—'}</td><td>{row.total??'—'}</td><td>{row.samples??'—'}</td><td>{row.coverage.replaceAll('-',' ')}</td>{daily&&<><td>{row.effectiveAt??'—'}</td><td>{row.window??'—'}</td></>}</tr>)}</tbody></table></div><nav className="app-table-pagination" aria-label={`${title} table pages`}><span>{(page-1)*50+1}–{Math.min(page*50,totalRows)} of {totalRows}</span><Button variant="outline" disabled={page<=1} onClick={()=>onPageChange(page-1)}>Previous</Button><Button variant="outline" disabled={page>=pages} onClick={()=>onPageChange(page+1)}>Next</Button></nav></div>;
}
