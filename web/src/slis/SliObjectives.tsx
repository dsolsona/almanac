import { useEffect, useState } from 'react';
import { StatusMark } from '../components/ui/index.ts';
import { sloClient, type CurrentStatus } from '../slos/client.ts';
import { vocabulary } from '../slos/statusVocabulary.ts';
import type { Sli } from './client.ts';

function targetPercent(value:string):string {
  const digits=(value.split('.')[1]??'').padEnd(2,'0'),fraction=digits.slice(2).replace(/0+$/,'');
  return `${Number(digits.slice(0,2))}${fraction?`.${fraction}`:''}%`;
}
export function SliObjectives({ projection, unavailable }: { projection:Sli|null; unavailable:boolean }) {
  const [results,setResults]=useState<Record<string,CurrentStatus|null>>({});
  const linked=projection?.linked_slos;
  useEffect(()=>{
    const request=new AbortController();
    setResults({});
    if(unavailable)return()=>request.abort();
    for(const item of linked?.items??[]) void sloClient.current(item.id,request.signal).then(value=>{
      if(!request.signal.aborted)setResults(current=>({...current,[item.id]:value}));
    }).catch(()=>{if(!request.signal.aborted)setResults(current=>({...current,[item.id]:null}));});
    return()=>request.abort();
  },[linked,unavailable]);
  return <section className="sli-detail-card" id="sli-objectives" aria-labelledby="sli-objectives-title">
    <header><h2 id="sli-objectives-title">{linked?`Used by ${linked.total_count} SLO${linked.total_count===1?'':'s'}`:'Linked SLOs'}</h2><span className="hint">Targets belong to each SLO</span></header>
    {unavailable&&linked&&<p className="sli-detail-note" role="alert">Linked objectives could not be refreshed. Showing previously loaded links; their membership may have changed. Refresh to try again.</p>}
    {linked?.items.length?<div className="sli-detail-table-wrap"><table className="sli-objectives-table"><thead><tr><th scope="col">SLO</th><th scope="col">Target · rolling window</th><th scope="col">Evaluation</th></tr></thead><tbody>{linked.items.map(item=>{
      const result=unavailable?null:results[item.id],words=result?vocabulary(result.urgency,result.leading_reason,false):null;
      const target=result?targetPercent(result.slo.target):null,days=result?result.slo.rolling_window_seconds/86400:null;
      return <tr key={item.id}><td><a href={`/slos/${item.id}`} data-app-route>{item.display_name}</a>{result&&<span className="desc">{result.slo.service_display_name} · {target} over {days} days</span>}</td><td className="mono">{result?`${target} · ${days} d`:'—'}</td><td>{words?<StatusMark tone={words.tone} label={words.label}/>:result===null?<StatusMark tone="none" label="Unavailable"/>:<span className="sli-muted" aria-busy="true">—</span>}</td></tr>;
    })}</tbody></table></div>:<p className="sli-detail-note">{unavailable?'Linked objectives could not be read. Refresh to try again.':linked?'No SLO targets this indicator yet.':'Linked objectives are loading.'}</p>}
    {!!linked?.items.length&&<p className="sli-detail-note">Open an SLO for its observed attainment, remaining budget and burn rate.{linked.total_count>linked.items.length?` Showing ${linked.items.length} of ${linked.total_count}.`:''}{Object.values(results).includes(null)?' Some evaluations could not be read; refresh to try again.':''}</p>}
  </section>;
}
