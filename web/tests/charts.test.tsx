import { describe, expect, it, vi } from 'vitest';
import { buildTimelineOption } from '../src/components/charts/timelineOption.ts';
import { validateTimeline, type Coverage, type TimelineChart } from '../src/components/charts/timelineModel.ts';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach } from 'vitest';
import { ExactValuesTable } from '../src/components/charts/ExactValuesTable.tsx';

afterEach(cleanup);
type SpawnSync = (command: string, args: string[], options: { encoding: 'utf8'; timeout: number }) => { error?: Error; status: number | null; stdout: string };
const nodeProcess = (globalThis as unknown as { process: { execPath: string; getBuiltinModule: (name: string) => unknown } }).process;

const chart: TimelineChart = {
  title: 'Synthetic', summary: 'Illustrative samples only', start: '2026-01-01T00:00:00Z', end: '2026-01-01T00:04:00Z', stepMs: 60_000,
  lanes: [{ id: 'availability', label: 'Availability', unit: '%', series: [{ id: 'sli', label: 'SLI', points: [
    { timestamp: '2026-01-01T00:00:00Z', value: '99.970000', coverage: 'complete' },
    { timestamp: '2026-01-01T00:01:00Z', value: null, coverage: 'missing' },
    { timestamp: '2026-01-01T00:03:00Z', value: '0.000000', coverage: 'partial' },
  ] }] }],
};
const palette = { axis: '#111111', labelText: '#444444', grid: '#222222', crosshair: '#333333', series: ['#123456', '#654321'] as const, surface: '#fafafa', surfaceBorder: '#bbbbbb', text: '#101010' };

describe('timeline model and options', () => {
  it('inserts nulls for absent steps and retains missing, zero and partial semantics', () => {
    const normalized = validateTimeline(chart);
    expect(normalized.series[0]?.data.map((item) => item.value)).toEqual([99.97, null, null, null]);
    expect(normalized.series[0]?.partial.map((item) => item.value)).toEqual([0]);
    expect(normalized.exactValues.get('2026-01-01T00:00:00.000Z')).toBe('99.970000');
    expect(normalized.series[0]?.tooltipData.map((item) => item.coverage)).toEqual(['complete', 'missing', 'partial']);
  });

  it('keeps known-empty, expired and not-applicable gaps distinct from numeric zero', () => {
    const states = ['known-empty', 'expired', 'not-applicable'] as const;
    const points = [
      { timestamp: '2026-01-01T00:00:00Z', value: '0', coverage: 'complete' as const },
      ...states.map((state, index) => ({ timestamp: `2026-01-01T00:0${index + 1}:00Z`, value: null, coverage: state })),
    ];
    const fixture: TimelineChart = { ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ id: 'sli', label: 'SLI', points }] }] };
    const normalized = validateTimeline(fixture);
    expect(normalized.series[0]?.data.map(point => point.value)).toEqual([0, null, null, null]);
    expect(normalized.series[0]?.tooltipData.map(point => point.coverage)).toEqual(['complete', ...states]);
    const option = buildTimelineOption(fixture, normalized, palette);
    const tooltip = option.tooltip as unknown as { formatter: (params: { axisValue: number }) => string };
    for (const [index, label] of ['Known empty', 'Expired history', 'Not applicable'].entries()) {
      expect(tooltip.formatter({ axisValue: Date.parse(points[index + 1]!.timestamp) })).toContain(label);
    }
    for (const state of states) {
      expect(() => validateTimeline({ ...fixture, lanes: [{ ...fixture.lanes[0]!, series: [{ id: 'sli', label: 'SLI', points: [{ ...points[0]!, coverage: state }] }] }] })).toThrow(/must not have a value/);
    }
    const rows = points.map(point => ({ ...point, lane: 'Availability', series: 'SLI', unit: '%' }));
    render(<ExactValuesTable rows={rows} page={1} totalRows={rows.length} onPageChange={() => undefined} />);
    expect(screen.getByText('0 %')).toBeVisible();
    expect(screen.getByText('Known empty')).toBeVisible();
    expect(screen.getByText('Expired history')).toBeVisible();
    expect(screen.getByText('Not applicable')).toBeVisible();
    expect(screen.getAllByText('—')).toHaveLength(3);
  });

  it('rejects invalid ranges, unsorted timestamps, nonfinite values and oversized series', () => {
    expect(() => validateTimeline({ ...chart, end: chart.start })).toThrow(/range/i);
    expect(() => validateTimeline({ ...chart, start: '2026-02-28T00:00:00Z', end: '2026-03-04T00:00:00Z', lanes: [{ ...chart.lanes[0]!, series: [{ ...chart.lanes[0]!.series[0]!, points: [{ timestamp: '2026-02-31T00:00:00Z', value: '1', coverage: 'complete' }] }] }] })).toThrow(/valid timestamp/i);
    const series = chart.lanes[0]!.series[0]!;
    expect(() => validateTimeline({ ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ ...series, points: [...series.points].reverse() }] }] })).toThrow(/ordered/i);
    expect(() => validateTimeline({ ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ ...series, points: [{ ...series.points[0]!, value: 'Infinity' }] }] }] })).toThrow(/finite/i);
    const points = Array.from({ length: 1501 }, (_, index) => ({ timestamp: new Date(Date.parse(chart.start) + index * 60_000).toISOString(), value: '1', coverage: 'complete' as const }));
    expect(() => validateTimeline({ ...chart, end: new Date(Date.parse(chart.start) + points.length * 60_000).toISOString(), lanes: [{ ...chart.lanes[0]!, series: [{ ...series, points }] }] })).toThrow(/1500/);
    expect(() => validateTimeline({ ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ ...series, points: [{ ...series.points[1]!, coverage: 'zero-traffic', value: '0' }] }] }] })).toThrow(/must not have a value/);
  });

  it('rejects a step that cannot advance timestamps near the end of the supported UTC range', async () => {
    const { spawnSync } = nodeProcess.getBuiltinModule('node:child_process') as { spawnSync: SpawnSync };
    const result = spawnSync(nodeProcess.execPath, ['tests/helpers/nonadvancing-gap-probe.mjs'], { encoding: 'utf8', timeout: 1_500 });
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(0);
    expect(result.stdout).toMatch(/expected gap timestamp cannot advance/i);
  });

  it('builds aligned lanes with one labelled bottom time axis and linked crosshair', () => {
    const fourLanes = { ...chart, lanes: Array.from({ length: 4 }, (_, index) => ({ ...chart.lanes[0]!, id: `lane-${index}` })) };
    const option = buildTimelineOption(fourLanes, validateTimeline(fourLanes), palette);
    const axes = Array.isArray(option.xAxis) ? option.xAxis : [option.xAxis];
    const yAxes = Array.isArray(option.yAxis) ? option.yAxis : [option.yAxis];
    const series = Array.isArray(option.series) ? option.series : [option.series];
    expect(axes).toHaveLength(4);
    expect(axes.map((axis) => (axis as { gridIndex?: number }).gridIndex)).toEqual([0, 1, 2, 3]);
    expect(axes.map((axis) => (axis as { axisLabel?: { show?: boolean } }).axisLabel?.show)).toEqual([false, false, false, true]);
    expect(yAxes.map((axis) => (axis as { splitNumber?: number }).splitNumber)).toEqual([2, 2, 2, 2]);
    expect(yAxes.map((axis) => (axis as { axisLabel?: { color?: string }; nameTextStyle?: { color?: string } }).axisLabel?.color)).toEqual(Array(4).fill(palette.labelText));
    expect(yAxes.map((axis) => (axis as { nameTextStyle?: { color?: string } }).nameTextStyle?.color)).toEqual(Array(4).fill(palette.labelText));
    expect(yAxes.map((axis) => (axis as { axisLabel?: { showMaxLabel?: boolean } }).axisLabel?.showMaxLabel)).toEqual([true, true, true, true]);
    expect(yAxes.map((axis) => (axis as { axisLabel?: { showMinLabel?: boolean } }).axisLabel?.showMinLabel)).toEqual([true, true, true, true]);
    expect(option.axisPointer).toMatchObject({ link: [{ xAxisIndex: 'all' }] });
    expect(option.color).toEqual(palette.series);
    expect(option.tooltip).toMatchObject({ backgroundColor: palette.surface, borderColor: palette.surfaceBorder, textStyle: { color: palette.text } });
    expect(series.some((item) => item && 'type' in item && item.type === 'line' && 'connectNulls' in item && item.connectNulls === false)).toBe(true);
    expect(option.animation).toBe(false);
  });

  it('uses the available plot height for one and four aligned lanes', () => {
    for (const count of [1, 4]) {
      const fixture = { ...chart, lanes: Array.from({ length: count }, (_, index) => ({ ...chart.lanes[0]!, id: `lane-${index}` })) };
      const option = buildTimelineOption(fixture, validateTimeline(fixture), palette);
      const grids = (Array.isArray(option.grid) ? option.grid : [option.grid]) as { top: string; height: string; left: number; right: number }[];
      expect(grids).toHaveLength(count);
      expect(grids[0]?.top).toBe('6%');
      expect(grids[0]?.height).toBe(count === 1 ? '78%' : '15%');
      expect(grids.map(grid => [grid.left, grid.right])).toEqual(Array(count).fill([66, 20]));
      for (let index = 1; index < grids.length; index++) {
        expect(parseFloat(grids[index]!.top)).toBeGreaterThan(parseFloat(grids[index - 1]!.top) + parseFloat(grids[index - 1]!.height));
      }
      expect(parseFloat(grids.at(-1)!.top) + parseFloat(grids.at(-1)!.height)).toBe(84);
    }
  });

  it('renders complete samples with visible symbols when null gaps isolate them', () => {
    const isolated: TimelineChart = { ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ id: 'isolated', label: 'Isolated', points: [
      { timestamp: '2026-01-01T00:00:00Z', value: '1', coverage: 'complete' },
      { timestamp: '2026-01-01T00:01:00Z', value: '1.5', coverage: 'partial' },
      { timestamp: '2026-01-01T00:02:00Z', value: '2', coverage: 'complete' },
      { timestamp: '2026-01-01T00:03:00Z', value: null, coverage: 'missing' },
    ] }] }] };
    const option = buildTimelineOption(isolated, validateTimeline(isolated), palette);
    const series = Array.isArray(option.series) ? option.series : [option.series];
    const isolatedMarkers = series.filter((item) => item && 'id' in item && typeof item.id === 'string' && item.id.endsWith('-isolated'));
    expect(isolatedMarkers).toHaveLength(1);
    expect(isolatedMarkers[0]).toMatchObject({ type: 'scatter', symbolSize: 9, data: [[Date.parse(isolated.start), 1], [Date.parse('2026-01-01T00:02:00Z'), 2]] });
    expect(series.find((item) => item && 'id' in item && typeof item.id === 'string' && item.id.endsWith('-partial'))).toMatchObject({ symbol: 'diamond' });
  });

  it('renders a single complete sample and a final boundary sample as markers', () => {
    const boundaryCases: TimelineChart[] = [
      { ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ id: 'single', label: 'Single', points: [
        { timestamp: '2026-01-01T00:02:00Z', value: '1', coverage: 'complete' },
      ] }] }] },
      { ...chart, lanes: [{ ...chart.lanes[0]!, series: [{ id: 'last', label: 'Last', points: [
        { timestamp: '2026-01-01T00:02:00Z', value: null, coverage: 'missing' },
        { timestamp: '2026-01-01T00:03:00Z', value: '2', coverage: 'complete' },
      ] }] }] },
    ];
    for (const boundaryCase of boundaryCases) {
      const option = buildTimelineOption(boundaryCase, validateTimeline(boundaryCase), palette);
      const series = Array.isArray(option.series) ? option.series : [option.series];
      expect(series.find((item) => item && 'id' in item && typeof item.id === 'string' && item.id.endsWith('-isolated'))).toMatchObject({
        type: 'scatter', data: [[Date.parse(boundaryCase.lanes[0]!.series[0]!.points.at(-1)!.timestamp), Number(boundaryCase.lanes[0]!.series[0]!.points.at(-1)!.value ?? 1)]],
      });
    }
  });

  it('assigns unique ECharts IDs when user series IDs collide across lanes and suffixes', () => {
    const partial = chart.lanes[0]!.series[0]!.points[2]!;
    const duplicateIds: TimelineChart = { ...chart, lanes: [
      { id: 'one', label: 'First', unit: '%', series: [
        { id: 'foo', label: 'Partial', points: [partial] },
        { id: 'foo-partial', label: 'Full', points: [{ timestamp: partial.timestamp, value: '1', coverage: 'complete' }] },
      ] },
      { id: 'two', label: 'Second', unit: '%', series: [{ id: 'foo', label: 'Repeated', points: [partial] }] },
    ] };
    const option = buildTimelineOption(duplicateIds, validateTimeline(duplicateIds), palette);
    const series = Array.isArray(option.series) ? option.series : [option.series];
    const ids = series.flatMap((item) => item && 'id' in item && typeof item.id === 'string' ? [item.id] : []);
    expect(ids).toHaveLength(6);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('rejects a page whose row count exceeds the remaining rows on that page', () => {
    const rows = Array.from({ length: 11 }, (_, index) => ({
      timestamp: `2026-01-01T00:${String(index).padStart(2, '0')}:00Z`, lane: 'Availability', series: 'SLI', unit: '%', value: '1', coverage: 'complete' as Coverage,
    }));
    render(<ExactValuesTable rows={rows} page={2} totalRows={60} onPageChange={() => undefined} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/Expected 10 rows/);
  });

  it('gives multiple exact-value tables unique accessible heading IDs', () => {
    const rows = [{ timestamp: '2026-01-01T00:00:00Z', lane: 'SLI', series: 'Example', unit: '%', value: '1.000000', coverage: 'complete' as const }];
    render(<><ExactValuesTable rows={rows} page={1} totalRows={1} onPageChange={() => undefined} /><ExactValuesTable rows={rows} page={1} totalRows={1} onPageChange={() => undefined} /></>);
    const headings = screen.getAllByRole('heading', { name: 'Exact values' });
    const ids = headings.map((heading) => heading.id);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(2);
    expect(headings.map((heading) => heading.parentElement?.getAttribute('aria-labelledby'))).toEqual(ids);
  });

  it('pages exact rows with bounded controls and rejects oversized or invalid pages', () => {
    const rows = Array.from({ length: 10 }, (_, index) => ({
      timestamp: `2026-01-01T00:${String(index + 50).padStart(2, '0')}:00Z`, lane: 'Availability', series: 'SLI', unit: '%',
      value: index === 2 || index === 3 ? null : index === 0 ? '99.970000' : '0.000000',
      coverage: (index === 1 ? 'partial' : index === 2 ? 'missing' : index === 3 ? 'zero-traffic' : 'complete') as Coverage,
    }));
    const change = vi.fn();
    render(<ExactValuesTable rows={rows} page={2} totalRows={60} onPageChange={change} />);
    expect(screen.getByText('99.970000 %')).toBeVisible();
    expect(screen.getByText('Partial')).toBeVisible();
    expect(screen.getByText('Missing')).toBeVisible();
    expect(screen.getByText('Zero traffic')).toBeVisible();
    expect(screen.getByText(/page 2 of 2/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Previous' }));
    expect(change).toHaveBeenCalledWith(1);
    cleanup();
    const oversizedRows = Array.from({ length: 51 }, (_, index) => ({ ...rows[index % rows.length]!, timestamp: `2026-01-01T00:${String(index).padStart(2, '0')}:00Z` }));
    render(<ExactValuesTable rows={oversizedRows} page={1} totalRows={100} onPageChange={change} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/more than 50/);
    cleanup();
    render(<ExactValuesTable rows={[]} page={3} totalRows={60} onPageChange={change} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/Page must be between 1 and 2/);
  });
});
