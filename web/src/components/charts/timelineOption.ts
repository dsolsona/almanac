import type { EChartsOption } from 'echarts';
import type { TimelineChart, ValidatedTimeline } from './timelineModel.ts';

export type TimelinePalette = {
  axis: string;
  labelText: string;
  grid: string;
  crosshair: string;
  series: readonly [string, string];
  surface: string;
  surfaceBorder: string;
  text: string;
  healthy?:string;
  breached?:string;
  sli?: string;
  muted?: string;
};

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!);
}

function coverageLabel(coverage: string): string {
  switch (coverage) {
    case 'zero-traffic': return 'Zero traffic';
    case 'known-empty': return 'Known empty';
    case 'expired': return 'Expired history';
    case 'not-applicable': return 'Not applicable';
    case 'stale': return 'Stale';
    case 'missing': return 'Missing';
    case 'partial': return 'Partial';
    default: return 'Complete';
  }
}

export function buildTimelineOption(chart: TimelineChart, model: ValidatedTimeline, palette: TimelinePalette, plotHeight?: number): EChartsOption {
  const { axis, grid, labelText, crosshair, series: colors } = palette;
  const lanes = chart.lanes.length;
  const top = 6;
  const gap = 6;
  const laneHeight = (78 - gap * (lanes - 1)) / lanes;
  const extraHeight=chart.lanes.reduce((sum,lane)=>sum+(lane.extra??0),0);
  const baseHeight=plotHeight===undefined?undefined:plotHeight-extraHeight;
  let reservedBefore=0;
  let referenceTop=0;
  const referenceLayout=chart.lanes.every(lane=>lane.height!==undefined);
  const grids = chart.lanes.map((lane, index) => {
    if(referenceLayout){const geometry={left:46,right:26,top:referenceTop+20,height:lane.height!-52};referenceTop+=lane.height!+(lane.extra??0);return geometry;}
    const geometry=extraHeight>0&&baseHeight!==undefined&&baseHeight>0
      ? {top:baseHeight*(top+index*(laneHeight+gap))/100+reservedBefore,height:baseHeight*laneHeight/100}
      : {top:`${top+index*(laneHeight+gap)}%`,height:`${laneHeight}%`};
    reservedBefore+=lane.extra??0;
    return {left:66,right:20,...geometry,containLabel:false};
  });
  const xAxis = chart.lanes.map((_, index) => ({
    type: 'time' as const, gridIndex: index, min: Date.parse(chart.start), max: Date.parse(chart.end),
    axisLine: { show:!referenceLayout||index===lanes-1,lineStyle: { color: axis } }, axisTick: { show: !referenceLayout&&index === lanes - 1 },
    axisLabel: { show: index === lanes - 1, color: labelText, hideOverlap: true,...(referenceLayout?{fontFamily:'monospace',fontSize:11}:{}), formatter: (value: number) => {
      const date = new Date(value);
      const short = Date.parse(chart.end) - Date.parse(chart.start) <= (referenceLayout?2:1)*86_400_000;
      if(referenceLayout)return date.toLocaleString('en-US',{month:'short',day:'numeric',...(short?{hour:'2-digit',minute:'2-digit',hour12:false}:{}),...(chart.timeZone==='local'?{}:{timeZone:'UTC'})});
      if(chart.timeZone==='local') return short ? date.toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'}) : date.toLocaleDateString(undefined,{year:'numeric',month:'2-digit',day:'2-digit'});
      return short ? `${String(date.getUTCHours()).padStart(2, '0')}:${String(date.getUTCMinutes()).padStart(2, '0')} UTC` : `${date.toISOString().slice(0, 10)} UTC`;
    } }, splitLine: { show: referenceLayout,lineStyle:{color:grid} }, axisPointer: { show: true, label: { show: false } },
  }));
  const yAxis = chart.lanes.map((lane, index) => {
    const values=model.series.filter(item=>item.lane===index).flatMap(item=>[...item.data.map(point=>point.value),...item.partial.map(point=>point.value)]).filter((value):value is number=>value!==null);
    const references=chart.referenceLines?.filter(line=>line.lane===lane.id).map(line=>Number(line.value)).filter(Number.isFinite)??[];
    const referenceExtent=references.length ? (()=>{
      const low=Math.min(...values,...references),high=Math.max(...values,...references);
      if(!values.length&&lane.unit==='%')return {min:Math.min(0,...references),max:Math.max(100,...references)};
      if(!values.length&&(lane.unit==='×'||lane.unit==='x'))return {min:Math.min(0,...references),max:Math.max(2,...references.map(value=>value*2))};
      if(values.length&&references.every(value=>value>=Math.min(...values)&&value<=Math.max(...values)))return {};
      const padding=Math.max((high-low)*0.05,Math.abs(low)*0.05,Math.abs(high)*0.05,1e-9);
      return {min:low-padding,max:high+padding};
    })() : {};
    return {
    type: 'value' as const, gridIndex: index, ...(referenceLayout&&values.length?{scale:!lane.range,...(lane.unit==='×'?referenceExtent:{})}:referenceExtent),...(lane.range?{min:lane.range[0],max:lane.range[1]}:{}), name: chart.showLaneNames===false?'':`${lane.label} (${lane.unit})`, nameLocation: 'middle' as const,
    nameGap: 46, nameTextStyle: { color: labelText, fontSize: 11 }, axisLabel: {
      color: labelText, fontSize: 10, margin: 4, hideOverlap: true,
      showMaxLabel: true, showMinLabel: true,
      formatter: (value:string|number)=>{const numeric=Number(value);return Number.isFinite(numeric)?referenceLayout?`${Number(numeric.toFixed(lane.decimals??2))}${lane.unit}`:String(Number(numeric.toPrecision(6))):String(value);},
    }, splitNumber: referenceLayout?5:2,
    axisLine: { show: !referenceLayout, lineStyle: { color: axis } }, axisTick: { show: false }, splitLine: { lineStyle: { color: referenceLayout?palette.surfaceBorder:grid } },
  };});
  const series = model.series.flatMap((item, index) => {
    const lane = chart.lanes[item.lane]!;
    const color = referenceLayout?(lane.id==='attainment'?palette.sli??colors[0]:colors[0]):colors[index % colors.length]!;
    const seriesId = `lane-${item.lane}-series-${item.seriesIndex}`;
    const isolated = item.data.filter((point, pointIndex) => point.value !== null &&
      (pointIndex === 0 || item.data[pointIndex - 1]?.value === null) &&
      (pointIndex === item.data.length - 1 || item.data[pointIndex + 1]?.value === null));
    // Copy slo-tracker's gap bands, using the actual coverage of each lane.
    // Partial numeric observations keep their separate markers; a band never
    // converts missing coverage into a successful line segment.
    const gaps:Array<[{xAxis:number;itemStyle:unknown},{xAxis:number}]>=[];
    if(referenceLayout&&!lane.bars){
      let start:number|undefined;
      for(const point of item.data){
        const unavailable=point.coverage!=='complete'&&point.coverage!=='stale';
        if(unavailable&&start===undefined)start=point.timestamp;
        if(!unavailable&&start!==undefined){gaps.push([{xAxis:start,itemStyle:{color:palette.muted??palette.surface,opacity:.65,decal:{symbol:'rect',dashArrayX:[1,0],dashArrayY:[2,4],rotation:-Math.PI/4,color:labelText}}},{xAxis:point.timestamp}]);start=undefined;}
      }
      if(start!==undefined)gaps.push([{xAxis:start,itemStyle:{color:palette.muted??palette.surface,opacity:.65,decal:{symbol:'rect',dashArrayX:[1,0],dashArrayY:[2,4],rotation:-Math.PI/4,color:labelText}}},{xAxis:Date.parse(chart.end)}]);
    }
    return [
      { id: `${seriesId}-line`, name: `${lane.label} · ${item.label}`, type: lane.bars?'bar' as const:'line' as const, xAxisIndex: item.lane, yAxisIndex: item.lane,
        showSymbol: false, connectNulls: false, animation: false, barMaxWidth:18,barMinHeight:lane.bars?3:undefined,itemStyle: { color,borderRadius:lane.bars?[3,3,0,0]:0 }, lineStyle: { color, width: referenceLayout?1.7:2 },
        ...(gaps.length?{markArea:{silent:true,label:{show:false},data:gaps}}:{}),
        ...(item.seriesIndex===0 && chart.referenceLines?.some(line=>line.lane===lane.id) ? {markLine:{silent:true,symbol:['none','none'],lineStyle:{color:palette.crosshair,type:'dashed',width:1},label:{color:palette.labelText,fontSize:10,position:referenceLayout?'insideEndTop':'insideEndBottom',align:'right'},data:chart.referenceLines.filter(line=>line.lane===lane.id).map(line=>({yAxis:Number(line.value),name:line.label,label:{formatter:line.label}}))}} : {}),
        data: item.data.map((point,position) => lane.colors?{value:[point.timestamp,point.value],itemStyle:{color:palette[lane.colors[position]??'healthy']??color,opacity:lane.colors[position]==='healthy'?.55:1}}:[point.timestamp, point.value]),
      },
      ...(!lane.bars&&isolated.length ? [{ id: `${seriesId}-isolated`, name: `${lane.label} · ${item.label}`, type: 'scatter' as const,
        xAxisIndex: item.lane, yAxisIndex: item.lane, symbolSize: 9, itemStyle: { color, borderColor: palette.surface, borderWidth: 2 },
        data: isolated.map((point) => [point.timestamp, point.value]),
      }] : []),
      ...(item.partial.length ? [{ id: `${seriesId}-partial`, name: `${lane.label} · ${item.label} · partial`, type: 'scatter' as const,
        xAxisIndex: item.lane, yAxisIndex: item.lane, symbol: 'diamond', symbolSize: 9, itemStyle: { color, borderColor: palette.surface, borderWidth: 2 },
        data: item.partial.map((point) => [point.timestamp, point.value]),
      }] : []),
    ];
  });
  return {
    useUTC: chart.timeZone!=='local',
    animation: false,
    aria: { enabled: true, label: { description: chart.summary } },
    color: colors,
    grid: grids,
    xAxis,
    yAxis,
    axisPointer: { link: [{ xAxisIndex: 'all' }], snap: true, lineStyle: { color: crosshair, type: 'dashed' } },
    tooltip: {
      className: 'timeline-chart-tooltip', backgroundColor: palette.surface, borderColor: palette.surfaceBorder,
      textStyle: { color: palette.text },
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (raw: unknown) => {
        const params = Array.isArray(raw) ? raw : [raw];
        const first = params[0] as { axisValue?: unknown } | undefined;
        const time = Number(first?.axisValue);
        const exactTime = new Date(time).toISOString();
        const rows: string[] = [];
        for (const lane of chart.lanes) for (const item of lane.series) {
          const point = item.points.find((sample) => Date.parse(sample.timestamp) === time);
          if (!point) continue;
          const label = coverageLabel(point.coverage);
          const display = point.value === null ? label : `${escapeHTML(point.value)} ${escapeHTML(lane.unit)} · ${label}`;
          rows.push(`<div>${escapeHTML(lane.label)} · ${escapeHTML(item.label)}: <strong>${display}</strong></div>`);
        }
        const label = chart.timeZone==='local' ? new Date(time).toLocaleString() : `${exactTime.slice(0, 16).replace('T', ' ')} UTC`;
        return `<div><strong>${label}</strong>${rows.join('') || '<div>No sample at this timestamp</div>'}</div>`;
      },
    },
    series,
  } as EChartsOption;
}
