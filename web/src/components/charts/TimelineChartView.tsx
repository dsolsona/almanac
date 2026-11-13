import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type PointerEvent } from 'react';
import { use } from 'echarts/core';
import { BarChart, LineChart, ScatterChart } from 'echarts/charts';
import { AriaComponent, GridComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
import { init } from 'echarts/core';
import type { ECharts } from 'echarts/core';
import { validateTimeline, type TimelineChart as TimelineChartData } from './timelineModel.ts';
import { TimelineChartHeader } from './TimelineChartHeader.tsx';
import { buildTimelineOption, type TimelinePalette } from './timelineOption.ts';
import './charts.css';
import { pointTime, inspectionIndex } from './pointInspection.ts';
import { traceChartRender } from '../../telemetry/client.ts';

use([BarChart, LineChart, ScatterChart, AriaComponent, GridComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent, SVGRenderer]);

function readTimelinePalette(): TimelinePalette {
  const style = getComputedStyle(document.documentElement);
  const color = (token: string) => style.getPropertyValue(token).trim();
  return {
    axis: color('--axis'), labelText: color('--text-3'), grid: color('--grid'), crosshair: color('--crosshair'),
    series: [color('--s1'), color('--s2')] as [string, string], surface: color('--surface'),
    surfaceBorder: color('--border'), text: color('--text'),healthy:color('--status-healthy'),breached:color('--status-breached'),critical:color('--crit'),sli:color('--s3'),muted:color('--surface-2'),
  };
}

export function TimelineChartView({ chart, showHeader = true, generation, inspection = false }: { chart: TimelineChartData; showHeader?: boolean; generation?: string; inspection?: boolean }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const titleId = useId(), inspectionId = useId();
  const frameRef = useRef<HTMLDivElement>(null), tooltipRef = useRef<HTMLDivElement>(null), cursorRef = useRef<HTMLDivElement>(null);
  const [selection, setSelection] = useState<{ index: number; pointerY?: number } | null>(null);
  const inspectionRef = useRef(inspection); inspectionRef.current = inspection;
  const instanceRef = useRef<ECharts | null>(null);
  const latestRef = useRef({ chart, generation, model: null as ReturnType<typeof validateTimeline> | null });
  const applyRef = useRef<() => void>(() => undefined);
  const result = useMemo(() => {
    try { return { model: validateTimeline(chart), error: null }; }
    catch (cause) { return { model: null, error: cause instanceof Error ? cause.message : 'Timeline data is invalid' }; }
  }, [chart]);
  latestRef.current = { chart, generation, model: result.model };

  useEffect(() => {
    const host = hostRef.current;
    if (!host || !result.model) return;
    const instance = init(host, undefined, { renderer: 'svg' });
    instanceRef.current = instance;
    let appliedGeneration='',appliedZone='UTC';
    instance.on('finished',()=>{
      host.dataset.renderedGeneration=appliedGeneration;
      host.dataset.renderedZone=appliedZone;
      host.dataset.chartReady='true';
    });
    const update = () => {
      const latest = latestRef.current;
      if (latest.model) {
        host.dataset.chartReady='false';
        appliedGeneration=latest.generation??'';appliedZone=latest.chart.timeZone??'UTC';
        traceChartRender(() => {
          const option = buildTimelineOption(latest.chart, latest.model!, readTimelinePalette(),host.clientHeight);
          if (inspectionRef.current) option.tooltip = { show: false };
          instance.setOption(option, true);
        });
      }
    };
    applyRef.current = update;
    let width=host.clientWidth,height=host.clientHeight;
    const resize = new ResizeObserver(() => {
      const nextWidth=host.clientWidth,nextHeight=host.clientHeight;
      if(nextWidth===width&&nextHeight===height)return;
      width=nextWidth;height=nextHeight;
      instance.resize();update();
    });
    resize.observe(host);
    const theme = new MutationObserver(update);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style'] });
    return () => {
      resize.disconnect();
      theme.disconnect();
      instance.dispose();
      instanceRef.current = null;
      applyRef.current = () => undefined;
    };
  }, [Boolean(result.model), inspection]);

  useEffect(() => { applyRef.current(); }, [chart, result, generation, inspection]);

  useEffect(() => { setSelection(null); }, [chart, generation, inspection]);
  useEffect(() => {
    if (!inspection) return;
    const close = (event: globalThis.PointerEvent) => { if (event.target instanceof Node && !frameRef.current?.contains(event.target)) setSelection(null); };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setSelection(null); };
    document.addEventListener('pointerdown', close); document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', escape); };
  }, [inspection]);
  const selectedTime = selection === null ? undefined : result.model?.timestamps[selection.index];
  const selectedRows = selectedTime === undefined ? [] : result.model!.series.map(series => ({
    id: `${series.lane}:${series.id}`, label: series.label, unit: chart.lanes[series.lane]!.unit,
    point: series.tooltipData.find(point => Date.parse(point.timestamp) === selectedTime),
  }));
  const selectedTimestamp = selectedRows.find(row => row.point)?.point?.timestamp ?? (selectedTime === undefined ? undefined : new Date(selectedTime).toISOString());
  useLayoutEffect(() => {
    const frame = frameRef.current, panel = tooltipRef.current, host = hostRef.current;
    if (!frame || !panel || !host || selectedTime === undefined) return;
    const bounds = frame.getBoundingClientRect(), plot = host.getBoundingClientRect();
    const pixel = instanceRef.current?.convertToPixel?.({ xAxisIndex: 0 }, selectedTime);
    const pointX = typeof pixel === 'number' && Number.isFinite(pixel) ? plot.left - bounds.left + pixel : bounds.width / 2;
    if (cursorRef.current) cursorRef.current.style.left = `${pointX}px`;
    const anchorY = selection?.pointerY === undefined ? plot.top - bounds.top + plot.height / 2 : selection.pointerY - bounds.top;
    const gap = 16, width = panel.offsetWidth, height = panel.offsetHeight;
    const right = pointX + gap, left = pointX - width - gap;
    const x = right + width <= bounds.width - 8 ? right : left >= 8 ? left : Math.max(0, Math.min(pointX - width / 2, bounds.width - width));
    const y = bounds.top + anchorY + gap + height <= innerHeight - 8 ? anchorY + gap : anchorY - height - gap;
    panel.style.left = `${Math.max(8 - bounds.left, Math.min(x, innerWidth - bounds.left - width - 8))}px`;
    panel.style.top = `${Math.max(8 - bounds.top, Math.min(y, innerHeight - bounds.top - height - 8))}px`;
  });
  function inspect(event: PointerEvent<HTMLDivElement>) {
    const host = hostRef.current, times = result.model?.timestamps;
    if (!host || !times?.length) return;
    const value = instanceRef.current?.convertFromPixel({ xAxisIndex: 0 }, event.clientX - host.getBoundingClientRect().left);
    if (typeof value !== 'number' || !Number.isFinite(value)) return;
    let index = 0;
    for (let next = 1; next < times.length; next++) if (Math.abs(times[next]! - value) < Math.abs(times[index]! - value)) index = next;
    setSelection({ index, pointerY: event.clientY });
  }
  const extraHeight=chart.lanes.reduce((sum,lane)=>sum+(lane.extra??0),0);
  const explicitHeight=chart.lanes.every(lane=>lane.height!==undefined)?chart.lanes.reduce((sum,lane)=>sum+lane.height!+(lane.extra??0),0):undefined;
  const plot = result.error ? <p className="timeline-chart__error" role="alert">Chart unavailable: {result.error}</p> : <div ref={hostRef} className="timeline-chart__plot" data-lanes={chart.lanes.length} style={explicitHeight?{height:explicitHeight}:extraHeight?{height:`calc(var(--timeline-base-height) + ${extraHeight}px)`}:undefined} role="img" aria-label={`${chart.title}. ${chart.summary}`} />;
  const content = !inspection || result.error ? plot : <div ref={frameRef} className="timeline-inspection" role="group" aria-label={`Inspect ${chart.title}. Use arrow keys, Home, End, Escape.`} tabIndex={result.model?.timestamps.length ? 0 : undefined} aria-describedby={selectedTime === undefined ? undefined : inspectionId}
    onPointerMove={event => { if (event.pointerType !== 'touch') inspect(event); }} onPointerDown={event => { if (event.pointerType === 'touch') inspect(event); }}
    onPointerLeave={event => { if (event.pointerType !== 'touch') setSelection(null); }} onBlur={() => setSelection(null)}
    onKeyDown={event => { if (!['Home', 'End', 'ArrowLeft', 'ArrowRight', 'Escape'].includes(event.key)) return; event.preventDefault(); const index = inspectionIndex(event.key, selection?.index ?? null, result.model?.timestamps.length ?? 0); setSelection(index === null ? null : { index }); }}>
    {plot}
    {selectedTime !== undefined && <div ref={cursorRef} className="timeline-inspection__cursor" aria-hidden="true" />}
    {selectedTime !== undefined && <div ref={tooltipRef} id={inspectionId} className="timeline-inspection__tooltip" role="tooltip">
      <time dateTime={selectedTimestamp}>{pointTime(selectedTimestamp!, chart.timeZone, true)}</time>
      {selectedRows.map(({ id, label, unit, point }) => <div key={id}><span>{label}</span><strong>{point?.value == null ? (point?.coverage ?? 'missing').replaceAll('-', ' ') : `${point.value} ${unit}`}</strong><small>{point?.value != null && point.coverage.replaceAll('-', ' ')}{point?.reason && ` · ${point.reason}`}</small>{point?.displayCoverage && <small>Displayed period includes {point.displayCoverage.states.join(', ')}</small>}</div>)}
    </div>}
    <span className="sr-only" aria-live="polite">{selectedTime === undefined ? '' : `${pointTime(selectedTimestamp!, chart.timeZone, true)} · ${selectedRows.map(row => `${row.label}: ${row.point?.value ?? row.point?.coverage ?? 'missing'} ${row.point?.value == null ? '' : row.unit}; ${row.point?.reason ?? ''}`).join(' · ')}`}</span>
  </div>;
  if (!showHeader) return <div className="timeline-chart__surface" role="group" aria-label={chart.title}>{content}</div>;
  return <section className="timeline-chart" aria-labelledby={titleId}>
    <div className="timeline-chart__header"><TimelineChartHeader chart={chart} titleId={titleId}/></div>
    <div className="timeline-chart__surface">{content}</div>
  </section>;
}
