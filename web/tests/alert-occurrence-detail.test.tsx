import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertOccurrenceDetail, type AlertOccurrenceDetailData } from '../src/alerts/AlertOccurrenceDetail.tsx';

vi.mock('echarts/core', async original => ({ ...await original<typeof import('echarts/core')>(), init: () => ({ on() {}, setOption() {}, resize() {}, dispose() {}, dispatchAction() {} }) }));
beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const window = { label: 'Recent window', seconds: 1800, start: '2026-11-12T10:12:00Z', end: '2026-11-12T10:42:00Z', bad: '375', total: '30000', observedErrorRate: '0.0125', allowedErrorRate: '0.001', burn: '12.5' };
const condition = { id: 'sustained', name: 'Sustained burn', outcome: 'matching' as const, explanation: 'Both windows meet the threshold.', threshold: '6', windows: [window, { ...window, label: 'Sustained window', seconds: 21600, start: '2026-11-12T04:42:00Z', bad: '2664', total: '360000', observedErrorRate: '0.0074', burn: '7.4' }] as const,
  chart: { title: 'Burn rate', summary: 'Exact supplied burn evidence.', start: '2026-11-12T09:42:00Z', end: '2026-11-12T10:42:00Z', stepMs: 300000, fillExpectedGaps: false, lanes: [{ id: 'burn', label: 'Burn', unit: '×', series: [{ id: 'recent', label: '30 min burn', points: [{ timestamp: '2026-11-12T10:42:00Z', value: '12.5', coverage: 'complete' as const }] }, { id: 'sustained', label: '6 h burn', points: [{ timestamp: '2026-11-12T10:42:00Z', value: '7.4', coverage: 'complete' as const }] }] }], referenceLines: [{ lane: 'burn', value: '6', label: '6× threshold' }] } };
const snapshot = { evaluatedAt: '2026-11-12T10:42:00Z', sloTarget: '0.999', sloWindowSeconds: 2592000, policyRevision: '7', sloRevision: '3', measurementRevision: '2', sourceGeneration: '9007199254740993', exclusionRevision: '4', calculationVersion: 'occurrences-v1', primaryConditionId: 'sustained', conditions: [condition] };
const data: AlertOccurrenceDetailData = {
  id: 'occurrence-1', version: '11', policyName: 'Checkout sustained burn', slo: { id: 'slo-1', name: 'Checkout availability', service: 'Checkout', href: '/slos/real-slo-id' }, state: 'firing', severity: 'medium', openedAt: '2026-11-12T10:32:00Z', detectedAt: '2026-11-12T10:33:00Z', reason: 'Both burn windows exceed the threshold.', processing: { state: 'current' }, latest: snapshot,
  original: { ...snapshot, evaluatedAt: '2026-11-12T10:32:00Z', sourceGeneration: '9007199254740990', conditions: [{ ...condition, windows: condition.windows.map((value, index) => ({ ...value, start: index === 0 ? '2026-11-12T10:02:00Z' : '2026-11-12T04:32:00Z', end: '2026-11-12T10:32:00Z', bad: index === 0 ? '180.000000000003' : '2160.000000000036', observedErrorRate: '0.0060000000000001', burn: '6.0000000000001' })) as unknown as typeof condition.windows }] },
  lifecycle: [{ id: 'fired', at: '2026-11-12T10:33:08.123456Z', effectiveAt: '2026-11-12T10:32:00Z', title: 'Alert fired', description: 'Both burn windows met the threshold in the same evaluation.' }],
};
const props = () => ({ data, now: '2026-11-12T10:42:00Z', backHref: '/alerts', canAcknowledge: true, onAcknowledge: vi.fn(), onRefresh: vi.fn() });

describe('selected alert detail', () => {
  it('uses one title, real SLO link, paired exact values and separate cards', async () => {
    const user = userEvent.setup(); render(<AlertOccurrenceDetail {...props()} />);
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Checkout sustained burn');
    expect(screen.getByRole('link', { name: 'Checkout availability' })).toHaveAttribute('href', '/slos/real-slo-id');
    expect(screen.getByText('Medium severity')).toBeVisible(); expect(screen.getByText('Firing', { exact: true })).toBeVisible();
    expect(screen.getByText('12.5×', { exact: true })).toBeVisible(); expect(screen.getByText('12.5×', { exact: true })).toHaveClass('is-matching'); expect(screen.getByText('7.4×', { exact: true })).toBeVisible();
    await user.click(screen.getByText('How this is calculated', { exact: true }));
    expect(screen.getByText(/375 bad.*30,000/)).toBeVisible();
    expect(screen.getAllByText('1.25%', { exact: true }).length).toBeGreaterThan(0);
    expect(screen.getAllByText('0.1%', { exact: true }).length).toBeGreaterThan(0);
    expect(screen.getByText('Lifecycle history', { exact: true })).toBeVisible();
    expect(screen.getByText(/10:33:08.123456 UTC/)).toBeVisible();
    expect(screen.getByText(/A 99.9% target allows 0.1% unsuccessful events/)).toBeVisible();
    for (const label of [/close alert/i, /assign/i, /notes/i, /destination/i]) expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
  });
  it('retains original and latest evidence separately, including exact revisions and bounds', async () => {
    const user = userEvent.setup(); render(<AlertOccurrenceDetail {...props()} />);
    await user.click(screen.getByText(/Original firing evidence/));
    const original = screen.getByRole('region', { name: 'Original firing evidence' });
    expect(within(original).getAllByText('6.0000000000001×', { exact: true })).toHaveLength(2);
    expect(within(original).getByText('9007199254740990')).toBeVisible();
    expect(within(original).getAllByText(/Observed error 0.60000000000001%/)).toHaveLength(2);
    expect(within(original).getByText('99.9%')).toBeVisible();
    expect(original.querySelector('time[datetime="2026-11-12T10:32:00Z"]')).not.toBeNull();
    await user.click(screen.getByText('Latest evidence provenance', { exact: true }));
    expect(screen.getByText('9007199254740993')).toBeVisible();
    expect(screen.getAllByText('occurrences-v1')).toHaveLength(2);
  });
  it('renders supplied non-matching and unavailable outcomes without deriving them from displayed burn', async () => {
    const user = userEvent.setup();
    const unavailable = { ...condition, id: 'unknown', name: 'Slow burn', outcome: 'unavailable' as const, explanation: 'Missing observations prevent recovery.', windows: condition.windows.map(value => ({ ...value, bad: null, total: null, burn: null, observedErrorRate: null, reason: 'Missing observations' })) as unknown as typeof condition.windows };
    render(<AlertOccurrenceDetail {...props()} data={{ ...data, state: 'acknowledged', acknowledged: { actor: 'Jamie Morgan', at: '2026-11-12T10:34:00Z' }, reason: 'Acknowledged; recovery is not confirmed.', latest: { ...snapshot, conditions: [{ ...condition, outcome: 'not_matching' }, unavailable] } }} />);
    expect(screen.getByText('Acknowledged', { exact: true })).toBeVisible();
    expect(within(screen.getByRole('group', { name: 'Inspect condition evidence' })).getByText('Not matching', { exact: true })).toBeVisible();
    expect(screen.getByText('12.5×', { exact: true })).not.toHaveClass('is-matching');
    await user.click(screen.getByRole('button', { name: /Slow burn/ }));
    expect(within(screen.getByRole('region', { name: 'Alert evidence' })).getByText('Missing observations prevent recovery.')).toBeVisible();
    expect(screen.queryByRole('button', { name: /^Acknowledge / })).not.toBeInTheDocument();
    expect(screen.queryByText('Resolved', { exact: true })).not.toBeInTheDocument();
  });
  it('preserves committed lifecycle and original due time while calculation waits', () => {
    render(<AlertOccurrenceDetail {...props()} data={{ ...data, processing: { state: 'waiting', dueAt: '2026-11-12T10:38:00Z', reason: 'Waiting for aggregate preparation', totalDelaySeconds: 240, aggregateWaitSeconds: 180 } }} />);
    expect(screen.getByText('Firing', { exact: true })).toBeVisible();
    expect(screen.getByText('Waiting for aggregate preparation')).toBeVisible();
    expect(document.querySelector('time[datetime="2026-11-12T10:38:00Z"]')).not.toBeNull();
    expect(screen.getByText(/Total delay/)).toBeVisible(); expect(screen.getByText(/Aggregate wait/)).toBeVisible();
  });
  it('requests the supplied occurrence/version and never optimistically acknowledges', async () => {
    const user = userEvent.setup(), input = props(); const view = render(<AlertOccurrenceDetail {...input} />);
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }));
    expect(input.onAcknowledge).toHaveBeenCalledExactlyOnceWith('occurrence-1', '11');
    expect(screen.getByText('Firing', { exact: true })).toBeVisible();
    view.rerender(<AlertOccurrenceDetail {...input} acknowledgement={{ pending: true }} />);
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
    view.rerender(<AlertOccurrenceDetail {...input} acknowledgement={{ pending: false, error: 'The alert changed. Refresh to inspect the committed state.' }} />);
    expect(screen.getByRole('alert')).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Refresh alert' })); expect(input.onRefresh).toHaveBeenCalledOnce();
    expect(screen.getByRole('heading', { level: 1 })).toHaveFocus();
  });
  it('returns acknowledgement focus to the title after supplied concurrent resolution', async () => {
    const user = userEvent.setup(), input = props(); const view = render(<AlertOccurrenceDetail {...input} />);
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }));
    view.rerender(<AlertOccurrenceDetail {...input} data={{ ...data, state: 'resolved', closedAt: '2026-11-12T10:42:00Z', reason: 'All conditions are not matching.' }} />);
    expect(screen.getByRole('heading', { level: 1 })).toHaveFocus();
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).not.toBeInTheDocument();
  });
  it('keeps viewer and administrative terminal states read-only without describing recovery', () => {
    render(<AlertOccurrenceDetail {...props()} canAcknowledge={false} data={{ ...data, state: 'disabled', terminalCause: 'SLO archived', reason: 'Disabled because the SLO was archived.' }} />);
    expect(screen.getByText('Disabled', { exact: true })).toBeVisible(); expect(screen.getByText('SLO archived', { exact: true })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).not.toBeInTheDocument();
    expect(screen.queryByText('Resolved', { exact: true })).not.toBeInTheDocument();
  });
});
