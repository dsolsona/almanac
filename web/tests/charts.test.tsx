import { describe, expect, it, vi } from 'vitest';
import { buildTimelineOption } from '../src/components/charts/timelineOption.ts';
import { validateTimeline, type Coverage, type TimelineChart } from '../src/components/charts/timelineModel.ts';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach } from 'vitest';
import { ExactValuesTable } from '../src/components/charts/ExactValuesTable.tsx';

afterEach(cleanup);

const chart: TimelineChart = {
  title: 'Synthetic', summary: 'Illustrative samples only', start: '2026-01-01T00:00:00Z', end: '2026-01-01T00:04:00Z', stepMs: 60_000,
  lanes: [{ id: 'availability', label: 'Availability', unit: '%', series: [{ id: 'sli', label: 'SLI', points: [
    { timestamp: '2026-01-01T00:00:00Z', value: '99.970000', coverage: 'complete' },
    { timestamp: '2026-01-01T00:01:00Z', value: null, coverage: 'missing' },
    { timestamp: '2026-01-01T00:03:00Z', value: '0.000000', coverage: 'partial' },
  ] }] }],
};
const palette = { axis: '#111111', grid: '#222222', crosshair: '#333333', series: ['#123456', '#654321'] as const, surface: '#fafafa', surfaceBorder: '#bbbbbb', text: '#101010' };

describe('timeline model and options', () => {
  it('inserts nulls for absent steps and retains missing, zero and partial semantics', () => {
    const normalized = validateTimeline(chart);
    expect(normalized.series[0]?.data.map((item) => item.value)).toEqual([99.97, null, null, null]);
    expect(normalized.series[0]?.partial.map((item) => item.value)).toEqual([0]);
    expect(normalized.exactValues.get('2026-01-01T00:00:00.000Z')).toBe('99.970000');
    expect(normalized.series[0]?.tooltipData.map((item) => item.coverage)).toEqual(['complete', 'missing', 'partial']);
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
    expect(yAxes.map((axis) => (axis as { axisLabel?: { showMaxLabel?: boolean } }).axisLabel?.showMaxLabel)).toEqual([true, true, true, true]);
    expect(yAxes.map((axis) => (axis as { axisLabel?: { showMinLabel?: boolean } }).axisLabel?.showMinLabel)).toEqual([true, true, true, true]);
    expect(option.axisPointer).toMatchObject({ link: [{ xAxisIndex: 'all' }] });
    expect(option.color).toEqual(palette.series);
    expect(option.tooltip).toMatchObject({ backgroundColor: palette.surface, borderColor: palette.surfaceBorder, textStyle: { color: palette.text } });
    expect(series.some((item) => item && 'type' in item && item.type === 'line' && 'connectNulls' in item && item.connectNulls === false)).toBe(true);
    expect(option.animation).toBe(false);
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
    expect(ids).toHaveLength(5);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('rejects a page whose row count exceeds the remaining rows on that page', () => {
    const rows = Array.from({ length: 11 }, (_, index) => ({
      timestamp: `2026-01-01T00:${String(index).padStart(2, '0')}:00Z`, lane: 'Availability', series: 'SLI', unit: '%', value: '1', coverage: 'complete' as Coverage,
    }));
    render(<ExactValuesTable rows={rows} page={2} totalRows={60} onPageChange={() => undefined} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/Expected 10 rows/);
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
