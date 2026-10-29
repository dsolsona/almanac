import { useEffect, useId, useMemo, useRef } from 'react';
import { use } from 'echarts/core';
import { LineChart, ScatterChart } from 'echarts/charts';
import { AriaComponent, GridComponent, MarkLineComponent, TooltipComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
import { init } from 'echarts/core';
import type { ECharts } from 'echarts/core';
import { validateTimeline, type TimelineChart as TimelineChartData } from './timelineModel.ts';
import { TimelineChartHeader } from './TimelineChartHeader.tsx';
import { buildTimelineOption, type TimelinePalette } from './timelineOption.ts';
import './charts.css';

use([LineChart, ScatterChart, AriaComponent, GridComponent, MarkLineComponent, TooltipComponent, SVGRenderer]);

function readTimelinePalette(): TimelinePalette {
  const style = getComputedStyle(document.documentElement);
  const color = (token: string) => style.getPropertyValue(token).trim();
  return {
    axis: color('--axis'), labelText: color('--text-3'), grid: color('--grid'), crosshair: color('--crosshair'),
    series: [color('--s1'), color('--s2')] as [string, string], surface: color('--surface'),
    surfaceBorder: color('--border'), text: color('--text'),
  };
}

export function TimelineChartView({ chart, showHeader = true }: { chart: TimelineChartData; showHeader?: boolean }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const instanceRef = useRef<ECharts | null>(null);
  const latestRef = useRef({ chart, model: null as ReturnType<typeof validateTimeline> | null });
  const applyRef = useRef<() => void>(() => undefined);
  const result = useMemo(() => {
    try { return { model: validateTimeline(chart), error: null }; }
    catch (cause) { return { model: null, error: cause instanceof Error ? cause.message : 'Timeline data is invalid' }; }
  }, [chart]);
  latestRef.current = { chart, model: result.model };

  useEffect(() => {
    const host = hostRef.current;
    if (!host || !result.model) return;
    const instance = init(host, undefined, { renderer: 'svg' });
    instanceRef.current = instance;
    const update = () => {
      const latest = latestRef.current;
      if (latest.model) instance.setOption(buildTimelineOption(latest.chart, latest.model, readTimelinePalette()), true);
    };
    applyRef.current = update;
    update();
    const resize = new ResizeObserver(() => instance.resize());
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
  }, [Boolean(result.model)]);

  useEffect(() => { applyRef.current(); }, [chart, result]);

  const content = result.error ? <p className="timeline-chart__error" role="alert">Chart unavailable: {result.error}</p> : <div ref={hostRef} className="timeline-chart__plot" data-lanes={chart.lanes.length} role="img" aria-label={`${chart.title}. ${chart.summary}`} />;
  if (!showHeader) return <div className="timeline-chart__surface" role="group" aria-label={chart.title}>{content}</div>;
  return <section className="timeline-chart" aria-labelledby={titleId}>
    <div className="timeline-chart__header"><TimelineChartHeader chart={chart} titleId={titleId}/></div>
    <div className="timeline-chart__surface">{content}</div>
  </section>;
}
