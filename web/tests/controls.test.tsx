import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Catalogue } from '../src/catalogue/Catalogue.tsx';
import { DatePicker, LoadingIndicator, Switch } from '../src/components/ui/index.ts';
import { addDays, addMonths, formatCalendarDate, parseCalendarDate } from '../src/components/ui/dateMath.ts';
import '../src/styles/global.css';

const originalShowModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');
const originalClose = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close');

beforeEach(() => {
  if (!originalShowModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  if (!originalClose) Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); } });
});

afterEach(() => {
  cleanup();
  if (!originalShowModal) Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal');
  if (!originalClose) Reflect.deleteProperty(HTMLDialogElement.prototype, 'close');
});

describe('feedback controls', () => {
  it('exposes a native accessible switch with a hint and immediate keyboard state', async () => {
    const user = userEvent.setup();
    render(<Switch label="Enable alerts" hint="Sends notifications" />);
    const control = screen.getByRole('switch', { name: 'Enable alerts' });
    expect(control).toHaveAttribute('type', 'checkbox');
    expect(control).toHaveAccessibleDescription('Sends notifications');
    await user.click(control);
    expect(control).toBeChecked();
    await user.keyboard(' ');
    expect(control).not.toBeChecked();
  });

  it('does not allow a disabled switch to change', async () => {
    const user = userEvent.setup();
    render(<Switch label="Locked alerts" checked disabled />);
    const control = screen.getByRole('switch', { name: 'Locked alerts' });
    await user.click(control);
    expect(control).toBeChecked();
    expect(control).toBeDisabled();
  });

  it('keeps visible switch state synchronized after native form reset', async () => {
    const user = userEvent.setup();
    render(<form><Switch label="Reset alerts" name="alerts" defaultChecked/><button type="reset">Reset form</button></form>);
    const control = screen.getByRole('switch', { name: 'Reset alerts' });
    await user.click(control);
    expect(control).not.toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Reset form' }));
    expect(control).toBeChecked();
    expect(screen.getByText('On', { exact: true })).toBeVisible();
  });

  it('validates leap days and rejects impossible or out-of-range dates', () => {
    expect(parseCalendarDate('2024-02-29')).toEqual({ year: 2024, month: 2, day: 29 });
    expect(parseCalendarDate('2025-02-29')).toBeNull();
    expect(parseCalendarDate('2024-02-30')).toBeNull();
    expect(parseCalendarDate('0000-01-01')).toBeNull();
    expect(parseCalendarDate('10000-01-01')).toBeNull();
  });

  it('clamps month changes and preserves years below 100', () => {
    expect(formatCalendarDate(addMonths({ year: 2024, month: 1, day: 31 }, 1))).toBe('2024-02-29');
    expect(formatCalendarDate(addMonths({ year: 2025, month: 1, day: 31 }, 1))).toBe('2025-02-28');
    expect(formatCalendarDate(addMonths({ year: 2024, month: 12, day: 31 }, 1))).toBe('2025-01-31');
    expect(formatCalendarDate(addDays({ year: 1, month: 1, day: 1 }, 1))).toBe('0001-01-02');
    expect(formatCalendarDate(addDays({ year: 99, month: 12, day: 31 }, 1))).toBe('0100-01-01');
  });

  it('focuses the selected calendar day and commits clamped navigation', async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<DatePicker label="Start date" value="2024-01-31" today="2024-02-10" onValueChange={onValueChange} />);
    await user.click(screen.getByRole('button', { name: /Start date/ }));
    expect(screen.getByRole('grid', { name: 'January 2024' })).toBeVisible();
    expect(screen.getByRole('button', { name: /January 31, 2024, selected/ })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Next month' }));
    expect(screen.getByRole('grid', { name: 'February 2024' })).toBeVisible();
    await user.keyboard('{Enter}');
    expect(onValueChange).toHaveBeenCalledWith('2024-02-29');
  });

  it('preserves the committed value when a calendar is cancelled or dismissed', async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<DatePicker label="Start date" value="2024-01-31" today="2024-02-10" onValueChange={onValueChange} />);
    const trigger = screen.getByRole('button', { name: /Start date/ });
    await user.click(trigger);
    await user.click(screen.getByRole('button', { name: 'Next month' }));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onValueChange).not.toHaveBeenCalled();
    expect(trigger).toHaveFocus();
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Choose Start date' });
    fireEvent(dialog, new Event('cancel', { bubbles: false, cancelable: true }));
    expect(onValueChange).not.toHaveBeenCalled();
    expect(trigger).toHaveFocus();
  });

  it('bounds navigation at year 0001 and 9999 and does not normalize invalid values', async () => {
    const user = userEvent.setup();
    const { rerender } = render(<DatePicker label="Start date" value="0001-01-01" today="0001-01-01" onValueChange={() => undefined} />);
    await user.click(screen.getByRole('button', { name: /Start date/ }));
    expect(screen.getByRole('button', { name: 'Previous month' })).toBeDisabled();
    await user.keyboard('{ArrowLeft}');
    expect(screen.getByRole('button', { name: /January 1, 1, selected/ })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Next month' }));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    rerender(<DatePicker label="Start date" value="9999-12-31" today="9999-12-31" onValueChange={() => undefined} />);
    await user.click(screen.getByRole('button', { name: /Start date/ }));
    expect(screen.getByRole('button', { name: 'Next month' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    rerender(<DatePicker label="Start date" value="2024-02-30" today="2024-02-29" onValueChange={() => undefined} />);
    expect(screen.getByRole('button', { name: /Start date/ })).toHaveTextContent('Choose date');
  });

  it('shows accessible indeterminate loading without a fake percentage', () => {
    const { container } = render(<LoadingIndicator label="Loading SLOs" />);
    expect(screen.getByRole('status', { name: 'Loading SLOs' })).toBeVisible();
    expect(container.querySelector('progress')).toBeNull();
  });

  it('gives repeated date fields unique hint ids and exposes their current values', () => {
    render(<><DatePicker label="Start date" value="2024-01-31" today="2024-01-31" hint="First hint" onValueChange={() => undefined}/><DatePicker label="Start date" value="2024-02-29" today="2024-01-31" hint="Second hint" onValueChange={() => undefined}/></>);
    const triggers = screen.getAllByRole('button', { name: /Start date/ });
    expect(triggers).toHaveLength(2);
    expect(triggers[0]).toHaveAccessibleDescription('First hint');
    expect(triggers[1]).toHaveAccessibleDescription('Second hint');
    expect(triggers[0].getAttribute('aria-labelledby')).not.toBe(triggers[1].getAttribute('aria-labelledby'));
  });

  it('requires an explicit action to finish the local save example', async () => {
    const user = userEvent.setup();
    render(<Catalogue />);
    await user.click(screen.getByRole('button', { name: 'Save local example' }));
    expect(screen.getByRole('button', { name: 'Finish local example' })).toBeEnabled();
    expect(screen.getByText(/Saving this local example/)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Finish local example' }));
    expect(screen.getByRole('button', { name: 'Save local example' })).toBeEnabled();
    expect(screen.getByText(/Saved locally for this page only/)).toBeVisible();
  });
});
