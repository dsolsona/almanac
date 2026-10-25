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
};

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!);
}

function coverageLabel(coverage: string): string {
  switch (coverage) {
    case 'zero-traffic': return 'Zero traffic';
    case 'missing': return 'Missing';
    case 'partial': return 'Partial';
    default: return 'Complete';
  }
}

export function buildTimelineOption(chart: TimelineChart, model: ValidatedTimeline, palette: TimelinePalette): EChartsOption {
  const { axis, grid, labelText, crosshair, series: colors } = palette;
  const lanes = chart.lanes.length;
  const grids = chart.lanes.map((_, index) => ({ left: 66, right: 20, top: `${6 + index * 22}%`, height: '16%', containLabel: false }));
  const xAxis = chart.lanes.map((_, index) => ({
    type: 'time' as const, gridIndex: index, min: Date.parse(chart.start), max: Date.parse(chart.end),
    axisLine: { lineStyle: { color: axis } }, axisTick: { show: index === lanes - 1 },
    axisLabel: { show: index === lanes - 1, color: labelText, hideOverlap: true, formatter: (value: number) => {
      const date = new Date(value);
      const short = Date.parse(chart.end) - Date.parse(chart.start) < 86_400_000;
      return short ? `${String(date.getUTCHours()).padStart(2, '0')}:${String(date.getUTCMinutes()).padStart(2, '0')} UTC` : `${date.toISOString().slice(0, 10)} UTC`;
    } }, splitLine: { show: false }, axisPointer: { show: true, label: { show: false } },
  }));
  const yAxis = chart.lanes.map((lane, index) => ({
    type: 'value' as const, gridIndex: index, name: `${lane.label} (${lane.unit})`, nameLocation: 'middle' as const,
    nameGap: 46, nameTextStyle: { color: labelText, fontSize: 11 }, axisLabel: {
      color: labelText, fontSize: 10, margin: 4, hideOverlap: true,
      showMaxLabel: true, showMinLabel: true,
    }, splitNumber: 2,
    axisLine: { show: true, lineStyle: { color: axis } }, axisTick: { show: false }, splitLine: { lineStyle: { color: grid } },
  }));
  const series = model.series.flatMap((item, index) => {
    const lane = chart.lanes[item.lane]!;
    const color = colors[index % colors.length]!;
    const seriesId = `lane-${item.lane}-series-${item.seriesIndex}`;
    const isolated = item.data.filter((point, pointIndex) => point.value !== null &&
      (pointIndex === 0 || item.data[pointIndex - 1]?.value === null) &&
      (pointIndex === item.data.length - 1 || item.data[pointIndex + 1]?.value === null));
    return [
      { id: `${seriesId}-line`, name: `${lane.label} · ${item.label}`, type: 'line' as const, xAxisIndex: item.lane, yAxisIndex: item.lane,
        showSymbol: false, connectNulls: false, animation: false, itemStyle: { color }, lineStyle: { color, width: 2 },
        data: item.data.map((point) => [point.timestamp, point.value]),
      },
      ...(isolated.length ? [{ id: `${seriesId}-isolated`, name: `${lane.label} · ${item.label}`, type: 'scatter' as const,
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
        const label = `${exactTime.slice(0, 16).replace('T', ' ')} UTC`;
        return `<div><strong>${label}</strong>${rows.join('') || '<div>No sample at this timestamp</div>'}</div>`;
      },
    },
    series,
  } as EChartsOption;
}
