export type Coverage = 'complete' | 'partial' | 'missing' | 'zero-traffic' | 'known-empty' | 'expired' | 'not-applicable' | 'stale';
export type TimelinePoint = { timestamp: string; value: string | null; coverage: Coverage };
export type TimelineSeries = { id: string; label: string; points: TimelinePoint[] };
export type TimelineLane = { id: string; label: string; unit: string; series: TimelineSeries[]; extra?: number };
export type TimelineChart = { title: string; summary: string; start: string; end: string; stepMs: number; lanes: TimelineLane[]; fillExpectedGaps?: boolean; timeZone?: 'UTC'|'local'; showLaneNames?:boolean; referenceLines?: {lane:string;value:string;label:string}[] };
export type PlotPoint = { timestamp: number; value: number | null; exact: string | null; coverage: Coverage };
export type PlotSeries = { lane: number; seriesIndex: number; id: string; label: string; data: PlotPoint[]; partial: PlotPoint[]; tooltipData: TimelinePoint[] };
export type ValidatedTimeline = { timestamps: number[]; series: PlotSeries[]; exactValues: Map<string, string> };

function timestamp(value: string, name: string): number {
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/.test(value)) throw new Error(`${name} must be a UTC ISO timestamp`);
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString().slice(0, 19) !== value.slice(0, 19)) throw new Error(`${name} must be a valid timestamp`);
  return parsed;
}

export function validateTimeline(chart: TimelineChart): ValidatedTimeline {
  const start = timestamp(chart.start, 'Start');
  const end = timestamp(chart.end, 'End');
  if (!(end > start)) throw new Error('Timeline range must have end after start');
  if (!Number.isFinite(chart.stepMs) || chart.stepMs <= 0) throw new Error('Timeline step must be positive');
  if (chart.lanes.length < 1 || chart.lanes.length > 4) throw new Error('Timeline must have between 1 and 4 lanes');
  if (chart.referenceLines?.some(line => !chart.lanes.some(lane=>lane.id===line.lane) || !Number.isFinite(Number(line.value)) || !line.label.trim())) throw new Error('Reference lines must name a chart lane and a finite value');
  const series: PlotSeries[] = [];
  const exactValues = new Map<string, string>();
  const timestamps = new Set<number>();
  chart.lanes.forEach((lane, laneIndex) => {
    if(lane.extra!==undefined&&(!Number.isFinite(lane.extra)||lane.extra<0))throw new Error('Reserved lane space must be a non-negative finite number');
    if (lane.series.length < 1 || lane.series.length > 2) throw new Error(`Lane ${lane.label} must have between 1 and 2 series`);
    lane.series.forEach((item, seriesIndex) => {
      if (item.points.length > 1500) throw new Error(`Series ${item.label} exceeds the 1500 plotted point limit`);
      let previous = -Infinity;
      const points: PlotPoint[] = [];
      const partial: PlotPoint[] = [];
      const tooltipData: TimelinePoint[] = [];
      for (const point of item.points) {
        const time = timestamp(point.timestamp, 'Sample');
        const endAlignedOperationalPoint=chart.fillExpectedGaps===false&&time===end;
        if (time < start || time >= end&&!endAlignedOperationalPoint) throw new Error(`Sample ${point.timestamp} is outside the timeline range`);
        if (time <= previous) throw new Error(`Series ${item.label} timestamps must be strictly ordered`);
        previous = time;
        timestamps.add(time);
        if (!['complete', 'partial', 'missing', 'zero-traffic', 'known-empty', 'expired', 'not-applicable', 'stale'].includes(point.coverage)) throw new Error(`Unsupported coverage state: ${String(point.coverage)}`);
        if (!['complete', 'partial', 'stale'].includes(point.coverage) && point.value !== null) throw new Error(`${point.coverage} samples must not have a value`);
        if ((point.coverage === 'complete' || point.coverage === 'stale') && point.value === null) throw new Error(`${point.coverage} samples require a value`);
        let numeric: number | null = null;
        if (point.value !== null) {
          if (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(point.value.trim())) throw new Error(`Sample values must be finite decimal numbers: ${point.value}`);
          numeric = Number(point.value);
          if (!Number.isFinite(numeric)) throw new Error(`Sample values must be finite numbers: ${point.value}`);
          exactValues.set(new Date(time).toISOString(), point.value);
        }
        const plot = { timestamp: time, value: numeric, exact: point.value, coverage: point.coverage };
        points.push(point.coverage === 'complete' || point.coverage === 'stale' ? plot : { ...plot, value: null });
        if ((point.coverage === 'partial' || point.coverage === 'stale') && numeric !== null) partial.push(plot);
        tooltipData.push(point);
      }
      const expanded: PlotPoint[] = [];
      if(chart.fillExpectedGaps===false) expanded.push(...points);
      for (let index = 0; chart.fillExpectedGaps !== false && index < points.length; index += 1) {
        const point = points[index]!;
        const prior = points[index - 1];
        if (prior) {
          let tickIndex = Math.floor((prior.timestamp - start) / chart.stepMs) + 1;
          let gapTime = start + tickIndex * chart.stepMs;
          if (!Number.isFinite(gapTime)) throw new Error(`Expected gap timestamp cannot be represented for series ${item.label}`);
          if (gapTime < point.timestamp && gapTime <= prior.timestamp) {
            throw new Error(`Expected gap timestamp cannot advance at the configured step for series ${item.label}`);
          }
          while (gapTime < point.timestamp) {
            if (gapTime <= (expanded.at(-1)?.timestamp ?? prior.timestamp)) {
              throw new Error(`Expected gap timestamp cannot advance at the configured step for series ${item.label}`);
            }
            if (expanded.length >= 1500) throw new Error(`Series ${item.label} exceeds the 1500 plotted point limit`);
            expanded.push({ timestamp: gapTime, value: null, exact: null, coverage: 'missing' });
            tickIndex += 1;
            const nextGapTime = start + tickIndex * chart.stepMs;
            if (Number.isNaN(nextGapTime)) throw new Error(`Expected gap timestamp cannot be represented for series ${item.label}`);
            if (nextGapTime < point.timestamp && nextGapTime <= gapTime) {
              throw new Error(`Expected gap timestamp cannot advance at the configured step for series ${item.label}`);
            }
            gapTime = nextGapTime;
          }
        }
        if (expanded.length >= 1500) throw new Error(`Series ${item.label} exceeds the 1500 plotted point limit`);
        expanded.push(point);
      }
      series.push({ lane: laneIndex, seriesIndex, id: item.id, label: item.label, data: expanded, partial, tooltipData });
    });
  });
  return { timestamps: [...timestamps].sort((a, b) => a - b), series, exactValues };
}
