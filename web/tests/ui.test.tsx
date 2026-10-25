import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi, afterEach } from 'vitest';
import { Button, SelectField, StatusBadge, TextField, ThemeToggle } from '../src/components/ui/index.ts';

afterEach(() => {
  cleanup();
  document.documentElement.classList.remove('dark');
  document.documentElement.style.colorScheme = '';
  localStorage.clear();
  vi.restoreAllMocks();
});

describe('shared controls', () => {
  it('does not invoke a disabled or pending button', async () => {
    const user = userEvent.setup();
    const action = vi.fn();
    render(<><Button disabled onClick={action}>Disabled action</Button><Button pending onClick={action}>Saving</Button></>);
    await user.click(screen.getByRole('button', { name: 'Disabled action' }));
    await user.click(screen.getByRole('button', { name: 'Saving' }));
    expect(action).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Saving' })).toHaveAttribute('aria-busy', 'true');
  });

  it('associates a labelled invalid field with its hint and error', () => {
    render(<TextField id="email" label="Email" hint="Work address" error="Enter a valid address" />);
    const field = screen.getByRole('textbox', { name: 'Email' });
    expect(field).toHaveAttribute('aria-invalid', 'true');
    const descriptions = (field.getAttribute('aria-describedby') ?? '').split(' ').map((id) => document.getElementById(id));
    expect(descriptions).toHaveLength(2);
    expect(descriptions.map((node) => node?.textContent)).toEqual(['Work address', 'Enter a valid address']);
  });

  it('keeps native select options selectable and disabled options unavailable', async () => {
    const user = userEvent.setup();
    render(<SelectField id="range" label="Range" options={[{ value: '', label: 'Choose one' }, { value: 'day', label: 'Day' }, { value: 'year', label: 'Year', disabled: true }]} />);
    const select = screen.getByRole('combobox', { name: 'Range' });
    await user.selectOptions(select, 'day');
    expect(select).toHaveValue('day');
    expect(screen.getByRole('option', { name: 'Year' })).toBeDisabled();
  });

  it('preserves native disabled field behavior', () => {
    render(<TextField id="locked" label="Locked value" value="Read only" disabled onChange={() => undefined} />);
    expect(screen.getByRole('textbox', { name: 'Locked value' })).toBeDisabled();
  });

  it('persists a theme choice and tolerates unavailable storage', async () => {
    const user = userEvent.setup();
    render(<ThemeToggle />);
    const toggle = screen.getByRole('button', { name: /switch to dark theme/i });
    await user.click(toggle);
    expect(document.documentElement).toHaveClass('dark');
    expect(toggle).toHaveAttribute('aria-pressed', 'true');
    expect(localStorage.getItem('almanac-theme')).toBe('dark');
  });

  it('restores the saved theme on mount', () => {
    localStorage.setItem('almanac-theme', 'dark');
    render(<ThemeToggle />);
    expect(document.documentElement).toHaveClass('dark');
    expect(screen.getByRole('button', { name: 'Switch to light theme' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('keeps a theme change when browser storage throws', async () => {
    const user = userEvent.setup();
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new DOMException('blocked'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('blocked'); });
    render(<ThemeToggle />);
    const toggle = screen.getByRole('button', { name: /switch to dark theme/i });
    await user.click(toggle);
    expect(document.documentElement).toHaveClass('dark');
    expect(toggle).toHaveAttribute('aria-pressed', 'true');
  });

  it('renders readable text for every operational status', () => {
    render(<div>{(['healthy', 'warning', 'breached', 'no-data', 'stale'] as const).map((status) => <StatusBadge key={status} status={status} />)}</div>);
    for (const label of ['Healthy', 'Warning', 'Breached', 'No data', 'Stale']) expect(screen.getByText(label)).toBeVisible();
    expect([...document.querySelectorAll('.ui-status__mark')].map((mark) => mark.textContent)).toEqual(['✓', '!', '×', '–', '↻']);
  });

});
