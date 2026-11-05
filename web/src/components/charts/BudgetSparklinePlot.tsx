import { use } from 'echarts/core';
import { LineChart, ScatterChart } from 'echarts/charts';
import { AriaComponent, GridComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
import { cssColor, useEChart } from './useEChart.ts';
import { buildBudgetSparklineOption, type BudgetSample } from './budgetSparklineOption.ts';

use([LineChart, ScatterChart, AriaComponent, GridComponent, SVGRenderer]);

export function BudgetSparklinePlot({samples,label,onReady}:{samples:BudgetSample[];label:string;onReady?:()=>void}) {
  const container=useEChart(()=>buildBudgetSparklineOption(samples,label,cssColor('--s1')),[samples,label],onReady);
  return <div className="slo-reference-spark" ref={container} role="img" aria-label={`${label}: ${samples.length} rolling budget samples; partial observations remain isolated`} style={{height:24}}/>;
}
