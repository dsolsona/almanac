import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertOccurrenceList, type AlertOccurrenceRow, type AlertListPage, emptyAlertListFilters } from '../src/alerts/AlertOccurrenceList.tsx';

const showModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');
const close = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close');
beforeEach(() => {
  if (!showModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  if (!close) Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); } });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); if (!showModal) Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal'); if (!close) Reflect.deleteProperty(HTMLDialogElement.prototype, 'close'); });

const fast = { id: 'fast', name: 'Fast burn', shortSeconds: 300, longSeconds: 3600, shortBurn: '22.8', longBurn: '18.6', threshold: '14.4' };
const sustained = { id: 'sustained', name: 'Sustained burn', shortSeconds: 1800, longSeconds: 21600, shortBurn: '12.5', longBurn: '7.4', threshold: '6' };
const occurrence: AlertOccurrenceRow = {
  id: 'occurrence-1', version: '7', policyName: 'Checkout burn', href: '#checkout-alert',
  slo: { id: 'slo-1', name: 'Checkout availability', service: 'Checkout', href: '#checkout-slo' },
  state: 'firing', severity: 'medium', openedAt: '2026-11-12T10:12:00Z', detectedAt: '2026-11-12T10:13:00Z',
  evidence: { at: '2026-11-12T10:41:00Z', matches: [fast, sustained] }, processing: { state: 'current' },
};
const page: AlertListPage = { kind: 'flat', items: [occurrence], total: 27, start: 1, end: 1, hasPrevious: false, hasNext: true };
function props() {
  return { page, filters: emptyAlertListFilters(), pageSize: 10, view: 'active' as const, grouped: false, now: '2026-11-12T10:42:00Z',
    slos: [{ id: 'slo-1', label: 'Checkout availability' }], canAcknowledge: true,
    onFiltersChange: vi.fn(), onGroupChange: vi.fn(), onPageSizeChange: vi.fn(), onPrevious: vi.fn(), onNext: vi.fn(), onRefresh: vi.fn(),
    onOpen: vi.fn(), onAcknowledge: vi.fn(), newPolicyHref: '#alert-policy' };
}

describe('selected alert occurrence list', () => {
  it('shows the evidence timestamp when recovery is unavailable but processing is current', () => {
    render(<AlertOccurrenceList {...props()} page={{ ...page, items: [{ ...occurrence, state: 'acknowledged', evidence: { at: occurrence.evidence.at, matches: [], unavailableReason: 'Missing observations prevent recovery' } }] }} />);
    expect(screen.getByText('Missing observations prevent recovery')).toBeVisible();
    expect(screen.getByText('Last evaluated 10:41 UTC')).toBeVisible();
    expect(screen.queryByText(/Evaluation due/)).not.toBeInTheDocument();
  });
  it('exposes effective closure separately from detection without requiring a hover or navigating the row', async () => {
    const user = userEvent.setup(), input = props();
    render(<AlertOccurrenceList {...input} view="history" page={{ ...page, items: [{ ...occurrence, state: 'resolved', closedAt: '2026-11-12T10:20:00Z', detectedAt: '2026-11-12T10:25:00Z' }] }} />);
    expect(screen.getByText('Closed 10:20 UTC')).toBeVisible();
    expect(screen.getByText('2026-11-12 10:20 UTC')).not.toBeVisible();
    await user.click(screen.getByText('Closed 10:20 UTC'));
    expect(screen.getByText('2026-11-12 10:20 UTC')).toBeVisible();
    expect(input.onOpen).not.toHaveBeenCalled();
    expect(screen.getByText('Detected 10:25 UTC')).toBeVisible();
    expect(screen.getByText('8m')).toBeVisible();
  });
  it('explains paired windows and other matches without changing policy severity', async () => {
    const user = userEvent.setup(); render(<AlertOccurrenceList {...props()} />);
    expect(screen.getByRole('link', { name: 'Checkout burn' })).toHaveAttribute('href', '#checkout-alert');
    expect(screen.getByRole('link', { name: 'Checkout availability' })).toHaveAttribute('href', '#checkout-slo');
    expect(screen.getByText('Medium severity')).toBeVisible();
    expect(screen.getByText('22.8×')).toBeVisible(); expect(screen.getByText('18.6×')).toBeVisible();
    expect(screen.getByText('Both ≥ 14.4× burn')).toBeVisible();
    await user.click(screen.getByText('1 more matching condition'));
    expect(screen.getByText('7.4×')).toBeVisible();
    expect(screen.queryByText(/needs acknowledgement/i)).not.toBeInTheDocument();
    expect(screen.queryByText('High severity')).not.toBeInTheDocument();
  });
  it('keeps row navigation independent of links, acknowledgement, modifiers and text selection', async () => {
    const user = userEvent.setup(), input = props(); render(<AlertOccurrenceList {...input} />);
    const row = screen.getByRole('link', { name: 'Checkout burn' }).closest('tr')!;
    await user.click(within(row).getByText('Medium severity'));
    expect(input.onOpen).toHaveBeenCalledExactlyOnceWith('#checkout-alert'); input.onOpen.mockClear();
    await user.click(within(row).getByRole('button', { name: 'Acknowledge Checkout burn' }));
    expect(input.onAcknowledge).toHaveBeenCalledExactlyOnceWith('occurrence-1', '7');
    expect(input.onOpen).not.toHaveBeenCalled();
    fireEvent.click(row, { ctrlKey: true }); expect(input.onOpen).not.toHaveBeenCalled();
    vi.spyOn(window, 'getSelection').mockReturnValue({ toString: () => 'selected text' } as Selection);
    fireEvent.click(row); expect(input.onOpen).not.toHaveBeenCalled();
  });
  it('uses supplied group identity and global totals rather than counting visible rows', () => {
    const input = props(), sameName = { ...occurrence, id: 'occurrence-2', slo: { ...occurrence.slo, id: 'slo-2' } };
    const grouped: AlertListPage = { ...page, kind: 'slo', end: 2, groups: [
      { slo: occurrence.slo, total: 18, firing: 12, items: [occurrence] },
      { slo: sameName.slo, total: 9, firing: 3, items: [sameName] },
    ] };
    render(<AlertOccurrenceList {...input} grouped page={grouped} />);
    expect(screen.getAllByRole('rowgroup')).toHaveLength(3);
    expect(screen.getByText('18 alerts · 12 firing')).toBeVisible();
    expect(screen.getByText('9 alerts · 3 firing')).toBeVisible();
    expect(screen.getByText('1–2 of 27 alerts')).toBeVisible();
    expect(screen.getAllByRole('button', { name: 'Acknowledge Checkout burn' })).toHaveLength(2);
  });
  it('preserves firing state and the original due time while calculation waits', () => {
    render(<AlertOccurrenceList {...props()} page={{ ...page, items: [{ ...occurrence, processing: { state: 'waiting', dueAt: '2026-11-12T10:38:00Z', reason: 'Waiting for calculation' } }] }} />);
    expect(screen.getByText('Firing')).toBeVisible(); expect(screen.getByText('Waiting for calculation')).toBeVisible();
    expect(screen.getByText(/due 10:38 UTC/)).toBeVisible(); expect(screen.getByText(/last evaluated 10:41 UTC/i)).toBeVisible();
    expect(screen.queryByText('Normal')).not.toBeInTheDocument();
  });
  it('does not fabricate matching conditions when unavailable evidence prevents recovery', () => {
    render(<AlertOccurrenceList {...props()} page={{ ...page, items: [{ ...occurrence, state: 'acknowledged',
      acknowledged: { actor: 'Jamie Morgan', at: '2026-11-12T10:20:00Z' }, evidence: { at: '2026-11-12T10:41:00Z', matches: [], unavailableReason: 'Missing observations prevent recovery' } }] }} />);
    expect(screen.getByText('Acknowledged')).toBeVisible(); expect(screen.getByText('Jamie Morgan')).toBeVisible();
    expect(screen.getByText('Missing observations prevent recovery')).toBeVisible();
    expect(screen.queryByText(/Both ≥/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Acknowledge Checkout/ })).not.toBeInTheDocument();
  });
  it('sends filter drafts only on Apply and returns focus when cancelled', async () => {
    const user = userEvent.setup(), input = props(); render(<AlertOccurrenceList {...input} />);
    const trigger = screen.getByRole('button', { name: 'Filter Alert' });
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Filter alert' });
    await user.type(within(dialog).getByLabelText('Alert name'), 'checkout');
    await user.selectOptions(within(dialog).getByLabelText('Severity'), 'high');
    expect(input.onFiltersChange).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(trigger).toHaveFocus(); expect(input.onFiltersChange).not.toHaveBeenCalled();
    await user.click(trigger);
    expect(within(dialog).getByLabelText('Alert name')).toHaveValue('');
    await user.type(within(dialog).getByLabelText('Alert name'), 'checkout');
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    expect(input.onFiltersChange).toHaveBeenCalledExactlyOnceWith({ ...emptyAlertListFilters(), policyName: 'checkout' });
  });
  it('restores focus to search after clearing active filters', async () => {
    const user = userEvent.setup(), input = props();
    const { rerender } = render(<AlertOccurrenceList {...input} filters={{ ...input.filters, policyName: 'checkout' }} />);
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(input.onFiltersChange).toHaveBeenCalledExactlyOnceWith(emptyAlertListFilters());
    rerender(<AlertOccurrenceList {...input} />);
    expect(screen.getByRole('searchbox', { name: 'Search alerts' })).toHaveFocus();
  });
  it('disables an in-flight acknowledgement and preserves explicit action failure', () => {
    render(<AlertOccurrenceList {...props()} acknowledgement={{ id: occurrence.id, pending: true, error: 'The alert changed. Refresh its current state.' }} />);
    expect(screen.getByRole('button', { name: 'Acknowledge Checkout burn' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('The alert changed. Refresh its current state.');
    expect(screen.getByText('Firing')).toBeVisible();
  });
  it('preserves a pending command when another row can be acknowledged', () => {
    render(<AlertOccurrenceList {...props()} page={{ ...page, items: [occurrence, { ...occurrence, id: 'another', policyName: 'Payment burn' }] }} acknowledgement={{ id: occurrence.id, pending: true }} />);
    expect(screen.getByRole('button', { name: 'Acknowledge Checkout burn' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Acknowledge Payment burn' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Acknowledge Payment burn' })).not.toHaveAttribute('aria-busy');
  });
  it('leaves viewers able to investigate but unable to acknowledge', () => {
    render(<AlertOccurrenceList {...props()} canAcknowledge={false} />);
    expect(screen.queryByRole('button', { name: /Acknowledge Checkout/ })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'View Checkout burn' })).toHaveAttribute('href', '#checkout-alert');
  });
  it('retains keyboard focus after a confirmed acknowledgement replaces its action', async () => {
    const user = userEvent.setup(), input = props();
    const { rerender } = render(<AlertOccurrenceList {...input} />);
    await user.click(screen.getByRole('button', { name: 'Acknowledge Checkout burn' }));
    rerender(<AlertOccurrenceList {...input} page={{ ...page, items: [{ ...occurrence, state: 'acknowledged' }] }} />);
    expect(screen.getByRole('link', { name: 'View Checkout burn' })).toHaveFocus();
  });
  it('returns focus to the page title if acknowledgement removes the filtered row', async () => {
    const user = userEvent.setup(), input = props();
    const { rerender } = render(<AlertOccurrenceList {...input} />);
    await user.click(screen.getByRole('button', { name: 'Acknowledge Checkout burn' }));
    rerender(<AlertOccurrenceList {...input} page={{ ...page, items: [], total: 0, start: 0, end: 0, hasNext: false }} />);
    expect(screen.getByRole('heading', { name: /^Alerts$/ })).toHaveFocus();
  });
  it('keeps loading and failed reads distinct from an empty successful page', () => {
    const input = props(), empty: AlertListPage = { ...page, items: [], total: 0, start: 0, end: 0, hasNext: false };
    const { rerender } = render(<AlertOccurrenceList {...input} page={empty} busy />);
    expect(screen.getByText('Loading alerts…')).toBeVisible();
    expect(screen.queryByText('No active alerts.')).not.toBeInTheDocument();
    rerender(<AlertOccurrenceList {...input} page={empty} error="Unable to read alerts." />);
    expect(screen.getByRole('alert')).toHaveTextContent('Unable to read alerts.');
    expect(screen.queryByText('No active alerts.')).not.toBeInTheDocument();
    rerender(<AlertOccurrenceList {...input} page={empty} />);
    expect(screen.getByText('No active alerts.')).toBeVisible();
  });
  it('distinguishes reconstructed recovered history, administrative closure and gaps', () => {
    const ended: AlertOccurrenceRow = { ...occurrence, state: 'resolved', closedAt: '2026-11-12T10:30:00Z', detectedAt: '2026-11-12T10:40:00Z', detectedAfterRecovery: true, reconstructed: true };
    render(<AlertOccurrenceList {...props()} view="history" page={{ ...page, items: [ended, { ...ended, id: 'archived', policyName: 'Archived policy', state: 'disabled', terminalCause: 'SLO archived', detectedAfterRecovery: false }] }} gaps={[{ id: 'gap', start: '2026-11-10T00:00:00Z', end: '2026-11-11T00:00:00Z', reason: 'Outside automatic catch-up horizon' }]} />);
    expect(screen.getByText('Detected after recovery')).toBeVisible();
    expect(screen.getAllByText('Reconstructed during catch-up')).toHaveLength(2);
    expect(screen.getByText('SLO archived')).toBeVisible(); expect(screen.getByText('Unreconstructed evaluation gap')).toBeVisible();
    expect(screen.queryByRole('button', { name: /Acknowledge/ })).not.toBeInTheDocument();
  });
});
