import { use } from 'echarts/core';
import { LineChart, ScatterChart } from 'echarts/charts';
import { AriaComponent, GridComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
import { cssColor, useEChart } from './useEChart.ts';
import './charts.css';

use([LineChart, ScatterChart, AriaComponent, GridComponent, SVGRenderer]);
type Sample={remaining_fraction:string|null;state:string};

// Geometry and chart treatment copied from slo-tracker's Sparkline. Numeric
// partial observations remain isolated rather than becoming a complete history.
export function buildBudgetSparklineOption(samples:Sample[],label:string,line:string) {
  const points=samples.map((sample,index)=>({index,value:sample.remaining_fraction===null?null:Number(sample.remaining_fraction)*100,complete:sample.state==='observed'||sample.state==='stale'}));
  return {animation:false,aria:{enabled:true,label:{description:label}},grid:{left:1,right:1,top:3,bottom:3},xAxis:{type:'value',show:false,min:0,max:Math.max(1,samples.length-1)},yAxis:{type:'value',show:false,scale:false},series:[
      {type:'line',showSymbol:false,connectNulls:false,lineStyle:{color:line,width:1.6},itemStyle:{color:line},areaStyle:{color:line,opacity:.08},data:points.map(point=>[point.index,point.complete?point.value:null])},
      {type:'scatter',symbol:'diamond',symbolSize:4,itemStyle:{color:line},data:points.filter(point=>!point.complete&&point.value!==null).map(point=>[point.index,point.value])},
    ]};
}
function BudgetSparklinePlot({samples,label}:{samples:Sample[];label:string}) {
  const container=useEChart(()=>buildBudgetSparklineOption(samples,label,cssColor('--s1')),[samples,label]);
  return <div className="slo-reference-spark" ref={container} role="img" aria-label={`${label}: ${samples.length} rolling budget samples; partial observations remain isolated`} style={{height:40}}/>;
}
export function SloBudgetSparkline({samples,label}:{samples:Sample[];label:string}) {
  return samples.some(sample=>sample.remaining_fraction!==null)
    ? <BudgetSparklinePlot samples={samples} label={label}/>
    : <span className="app-slo-history-empty" role="img" aria-label={`${label}: no budget history`}>No budget history</span>;
}
