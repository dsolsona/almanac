import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AlertPolicyForm } from '../src/alerts/AlertPolicyForm.tsx';

const slos = [{ id: 'checkout', label: 'Checkout availability', detail: 'Checkout · 99.9% target · 30 days', target: '0.999', cadenceSeconds: 60, windowSeconds: 2592000 }];
afterEach(cleanup);

async function fillIdentity(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Alert policy name'), 'Checkout burn');
  await user.click(screen.getByRole('combobox', { name: 'SLO' }));
  await user.type(screen.getByRole('combobox', { name: 'SLO' }), 'check');
  await user.keyboard('{ArrowDown}{Enter}');
}

describe('approved alert policy form', () => {
  it('shows the compact text defaults and saves a policy draft without creating an occurrence', async () => {
    const user = userEvent.setup(), save = vi.fn();
    render(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} />);
    expect(screen.getAllByRole('textbox')).toHaveLength(1);
    expect(screen.getByText(/both windows of any one condition/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'Edit short window for Fast burn' })).toHaveTextContent('5 min');
    expect(screen.getByRole('button', { name: 'Edit long window for Sustained burn' })).toHaveTextContent('6 h');
    await fillIdentity(user);
    expect(screen.getByRole('button', { name: 'Change SLO' })).toHaveFocus();
    await user.click(screen.getByRole('radio', { name: 'Medium' }));
    await user.click(screen.getByRole('button', { name: 'Create alert policy' }));
    expect(save).toHaveBeenCalledOnce();
    expect(save.mock.calls[0]![0]).toMatchObject({ name: 'Checkout burn', sloId: 'checkout', severity: 'medium', conditions: [
      { shortSeconds: 300, longSeconds: 3600, threshold: '14.4' },
      { shortSeconds: 1800, longSeconds: 21600, threshold: '6' },
    ] });
    expect(screen.queryByText('Firing')).not.toBeInTheDocument();
  });
  it('edits duration text with units, cancels with Escape, and keeps a reachable keyboard position', async () => {
    const user = userEvent.setup();
    render(<AlertPolicyForm slos={slos} onSave={() => undefined} onCancel={() => undefined} />);
    await user.click(screen.getByRole('button', { name: 'Edit short window for Fast burn' }));
    let input = screen.getByRole('textbox', { name: 'Short window for Fast burn' });
    expect(input).toHaveFocus();
    await user.clear(input); await user.type(input, '10 minutes'); await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Edit short window for Custom condition' })).toHaveTextContent('10 min');
    expect(screen.getByRole('button', { name: 'Edit short window for Custom condition' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Edit long window for Custom condition' }));
    input = screen.getByRole('textbox', { name: 'Long window for Custom condition' });
    await user.clear(input); await user.type(input, '2 days'); await user.keyboard('{Escape}');
    expect(screen.getByRole('button', { name: 'Edit long window for Custom condition' })).toHaveTextContent('1 h');
    expect(screen.getByRole('button', { name: 'Edit long window for Custom condition' })).toHaveFocus();
    expect(screen.queryByRole('combobox', { name: /unit/i })).not.toBeInTheDocument();
  });
  it('retains the original unit through each keystroke of a bare duration', async () => {
    const user = userEvent.setup();
    render(<AlertPolicyForm slos={slos} onSave={() => undefined} onCancel={() => undefined} />);
    await user.click(screen.getByRole('button', { name: 'Edit long window for Fast burn' }));
    const input = screen.getByRole('textbox', { name: 'Long window for Fast burn' });
    await user.clear(input); await user.type(input, '240'); await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Edit long window for Custom condition' })).toHaveTextContent('10 d');
  });
  it('preserves invalid edits and reopens the first invalid field instead of submitting a hidden old value', async () => {
    const user = userEvent.setup(), save = vi.fn();
    render(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} />);
    await fillIdentity(user);
    await user.click(screen.getByRole('button', { name: 'Edit short window for Fast burn' }));
    const input = screen.getByRole('textbox', { name: 'Short window for Fast burn' });
    await user.clear(input); await user.type(input, 'five min'); await user.tab();
    expect(screen.getByRole('button', { name: 'Edit short window for Custom condition' })).toHaveTextContent('five min');
    await user.click(screen.getByRole('button', { name: 'Create alert policy' }));
    expect(save).not.toHaveBeenCalled();
    const invalid = screen.getByRole('textbox', { name: 'Short window for Custom condition' });
    expect(invalid).toHaveFocus(); expect(invalid).toHaveAttribute('aria-invalid', 'true');
    expect(invalid).toHaveAccessibleDescription(/whole minutes/i);
    await user.clear(invalid); await user.type(invalid, '5 min'); await user.keyboard('{Enter}');
    await user.click(screen.getByRole('button', { name: 'Create alert policy' }));
    expect(save).toHaveBeenCalledOnce();
  });
  it('adds custom conditions, suppresses duplicate presets and keeps one condition', async () => {
    const user = userEvent.setup();
    render(<AlertPolicyForm slos={slos} onSave={() => undefined} onCancel={() => undefined} />);
    await user.click(screen.getByRole('button', { name: 'Add condition' }));
    const menu = screen.getByRole('dialog', { name: 'Add a condition' });
    expect(within(menu).getByRole('button', { name: /Fast burn/ })).toBeDisabled();
    expect(within(menu).getByRole('button', { name: /Sustained burn/ })).toBeDisabled();
    await user.click(within(menu).getByRole('button', { name: /Custom condition/ }));
    expect(screen.getByRole('textbox', { name: 'Short window for Custom condition' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.getAllByRole('button', { name: /Remove .* condition|Remove Fast burn|Remove Sustained burn/ })).toHaveLength(3);
    await user.click(screen.getByRole('button', { name: 'Remove Custom condition' }));
    await user.click(screen.getByRole('button', { name: 'Remove Fast burn' }));
    expect(screen.getByRole('button', { name: 'Remove Sustained burn' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Add condition' })).toHaveFocus();
  });
  it('preserves values on failure and prevents repeat activation while pending', async () => {
    const user = userEvent.setup(), save = vi.fn();
    const { rerender } = render(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} />);
    await fillIdentity(user);
    rerender(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} pending error="The policy could not be saved." />);
    expect(screen.getByLabelText('Alert policy name')).toHaveValue('Checkout burn');
    expect(screen.getByRole('button', { name: 'Create alert policy' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('The policy could not be saved.');
  });
  it('closes the condition menu when its trigger is activated a second time', async () => {
    const user = userEvent.setup();
    render(<AlertPolicyForm slos={slos} onSave={() => undefined} onCancel={() => undefined} />);
    const trigger = screen.getByRole('button', { name: 'Add condition' });
    await user.click(trigger);
    expect(screen.getByRole('dialog', { name: 'Add a condition' })).toBeVisible();
    await user.click(trigger);
    expect(screen.queryByRole('dialog', { name: 'Add a condition' })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });
  it('focuses Change SLO if a selected option becomes unsupported', async () => {
    const user = userEvent.setup(), save = vi.fn();
    const { rerender } = render(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} />);
    await fillIdentity(user);
    rerender(<AlertPolicyForm slos={slos.map(slo => ({ ...slo, disabled: true }))} onSave={save} onCancel={() => undefined} />);
    await user.click(screen.getByRole('button', { name: 'Create alert policy' }));
    expect(save).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Change SLO' })).toHaveFocus());
  });
  it('focuses a newly received save error without discarding or trapping the draft', async () => {
    const user = userEvent.setup(), save = vi.fn();
    const { rerender } = render(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} />);
    await fillIdentity(user);
    rerender(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} error="The policy could not be saved." />);
    expect(screen.getByRole('alert')).toHaveFocus();
    expect(screen.getByLabelText('Alert policy name')).toHaveValue('Checkout burn');
    await user.click(screen.getByLabelText('Alert policy name'));
    rerender(<AlertPolicyForm slos={slos} onSave={save} onCancel={() => undefined} error="The policy could not be saved." />);
    expect(screen.getByLabelText('Alert policy name')).toHaveFocus();
  });
});
