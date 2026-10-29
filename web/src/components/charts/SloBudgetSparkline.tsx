import './charts.css';

type Sample={remaining_fraction:string|null;state:string};
const missingStates=['partial','missing','expired','not_applicable','zero_traffic','known_empty'];

function pathFor(samples:Sample[]):string {
  const numeric=samples.map((sample,index)=>({index,value:sample.remaining_fraction===null||missingStates.includes(sample.state)?null:Number(sample.remaining_fraction)}));
  const values=numeric.flatMap(point=>point.value!==null&&Number.isFinite(point.value)?[point.value]:[]);
  if(values.length<2)return '';
  const min=Math.min(...values),max=Math.max(...values),span=max-min||1;
  let active=false;
  return numeric.map(point=>{
    if(point.value===null||!Number.isFinite(point.value)){active=false;return '';}
    const x=samples.length===1?50:point.index/(samples.length-1)*100;
    const y=26-(point.value-min)/span*24;
    const command=active?'L':'M';active=true;return `${command}${x.toFixed(2)},${y.toFixed(2)}`;
  }).filter(Boolean).join(' ');
}

export function SloBudgetSparkline({samples,label}:{samples:Sample[];label:string}) {
  const path=pathFor(samples);
  const gaps=samples.some(sample=>sample.remaining_fraction===null||missingStates.includes(sample.state));
  const partial=samples.some(sample=>sample.state==='partial');
  const gapSummary=partial?'with partial-coverage gaps':gaps?'with explicit gaps':'';
  return <svg className="slo-budget-sparkline" viewBox="0 0 100 28" role="img" aria-label={`${label}: ${samples.length} rolling budget samples${gapSummary?`, ${gapSummary}`:''}`}>
    <line x1="0" y1="26" x2="100" y2="26" className="slo-budget-sparkline__baseline"/>
    {path&&<path d={path} className="slo-budget-sparkline__line"/>}
    {!path&&<text x="50" y="18" textAnchor="middle">No history</text>}
  </svg>;
}
