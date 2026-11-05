import { ModuleView } from '../ModuleView.tsx';
import type { BudgetSample } from './budgetSparklineOption.ts';
import './charts.css';

export { buildBudgetSparklineOption } from './budgetSparklineOption.ts';
const loadPlot=()=>import('./BudgetSparklinePlot.tsx').then(module=>({default:module.BudgetSparklinePlot}));

export function SloBudgetSparkline({samples,label,onReady}:{samples:BudgetSample[];label:string;onReady?:()=>void}) {
  return samples.some(sample=>sample.remaining_fraction!==null)
    ? <ModuleView load={loadPlot} componentProps={{samples,label,onReady}} fallback={<span className="slo-reference-spark" role="status" style={{display:'block',height:24}}>Loading history…</span>}/>
    : <span className="app-slo-history-empty" role="img" aria-label={`${label}: no budget history`}>No budget history</span>;
}
