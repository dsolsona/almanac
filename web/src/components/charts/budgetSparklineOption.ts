export type BudgetSample={remaining_fraction:string|null;state:string;display_coverage?:{states:string[]}};

// Retained sparkline geometry; partial observations remain isolated.
export function buildBudgetSparklineOption(samples:BudgetSample[],label:string,line:string) {
  const points=samples.map((sample,index)=>({index,value:sample.remaining_fraction===null?null:Number(sample.remaining_fraction)*100,complete:(sample.state==='observed'||sample.state==='stale')&&!sample.display_coverage?.states.some(state=>state!=='observed'&&state!=='stale')}));
  return {animation:false,aria:{enabled:true,label:{description:label}},grid:{left:1,right:1,top:3,bottom:3},xAxis:{type:'value',show:false,min:0,max:Math.max(1,samples.length-1)},yAxis:{type:'value',show:false,scale:false},series:[
      {type:'line',showSymbol:false,connectNulls:false,lineStyle:{color:line,width:1.6},itemStyle:{color:line},areaStyle:{color:line,opacity:.08},data:points.map(point=>[point.index,point.complete?point.value:null])},
      {type:'scatter',symbol:'diamond',symbolSize:4,itemStyle:{color:line},data:points.filter(point=>!point.complete&&point.value!==null).map(point=>[point.index,point.value])},
    ]};
}
