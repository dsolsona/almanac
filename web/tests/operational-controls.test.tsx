import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ChoiceChips, Combobox, Pager, SearchField, SegmentedControl, StatusMark, TextareaField, type ComboboxOption } from '../src/components/ui/index.ts';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const sources: ComboboxOption[] = [
  { id: 'a', label: 'local-victoriametrics', tone: 'healthy', status: 'Healthy', detail: 'agent Local · connected' },
  { id: 'b', label: 'prod-prometheus-eu', tone: 'healthy', status: 'Healthy', detail: 'agent eu-west-1 · connected', keywords: 'eu' },
  { id: 'c', label: 'staging-mimir', tone: 'none', status: 'No agent', detail: 'no agent assigned', disabled: true },
];

function ComboboxHarness({ noMatch }: { noMatch?: (term: string) => void }) {
  const [value, setValue] = useState<string | null>(null);
  return <Combobox id="ds" label="Datasource" options={sources} value={value} onChange={setValue} noMatch={noMatch ? { text: (term) => `Nothing for ${term}`, action: { label: (term) => `Create ${term}`, onSelect: noMatch } } : undefined} />;
}
function ChipsHarness() {
  const [value, setValue] = useState('99.9');
  return <><ChoiceChips id="target" label="Target" value={value} onChange={setValue} options={[{ value: '99', label: '99%' }, { value: '99.9', label: '99.9%' }]} custom={{ unit: '%', format: (v) => `${v}%`, validate: (v) => Number(v) < 100 ? undefined : 'Below 100 only.' }} /><output>{value}</output></>;
}
function SegmentedHarness() {
  const [value, setValue] = useState('all');
  return <><SegmentedControl label="Filter" value={value} onChange={setValue} options={[{ value: 'all', label: 'All', count: 3 }, { value: 'off', label: 'Off', disabled: true }, { value: 'hot', label: 'Hot' }]} /><output>{value}</output></>;
}

describe('operational controls', () => {
  it('renders a status mark with its label and reason without relying on colour alone', () => {
    render(<StatusMark tone="breached" label="Breached" reason="window below target" />);
    expect(screen.getByText('Breached')).toBeVisible();
    expect(screen.getByText('window below target')).toBeVisible();
    expect(document.querySelector('.ui-mark--breached .ui-mark__shape')).toHaveAttribute('aria-hidden', 'true');
  });

  it('segmented control selects with click and arrow keys and skips disabled options', async () => {
    const user = userEvent.setup();
    render(<SegmentedHarness />);
    const group = screen.getByRole('radiogroup', { name: 'Filter' });
    const all = within(group).getByRole('radio', { name: 'All 3' });
    expect(all).toHaveAttribute('aria-checked', 'true');
    expect(within(group).getByRole('radio', { name: 'Off' })).toBeDisabled();
    all.focus();
    await user.keyboard('{ArrowRight}');
    expect(within(group).getByRole('radio', { name: 'Hot' })).toHaveFocus();
    expect(document.querySelector('output')!.textContent).toBe('hot');
    await user.click(all);
    expect(document.querySelector('output')!.textContent).toBe('all');
  });

  it('chips apply a custom value as typed, relabel the chip and report an invalid value', async () => {
    const user = userEvent.setup();
    render(<ChipsHarness />);
    const custom = screen.getByRole('radio', { name: 'Custom…' });
    await user.click(custom);
    const input = screen.getByRole('textbox', { name: 'Custom target' });
    expect(input).toHaveFocus();
    await user.type(input, '99.7');
    expect(document.querySelector('output')!.textContent).toBe('99.7');
    await user.keyboard('{Enter}');
    expect(screen.getByRole('radio', { name: 'Custom · 99.7%' })).toHaveAttribute('aria-checked', 'true');
    expect(input).not.toBeVisible();
    await user.click(screen.getByRole('radio', { name: 'Custom · 99.7%' }));
    await user.clear(input);
    await user.type(input, '100');
    expect(screen.getByRole('alert')).toHaveTextContent('Below 100 only.');
    expect(document.querySelector('output')!.textContent).toBe('100');
    expect(screen.getByRole('radio', { name: 'Custom…' })).toHaveAttribute('aria-checked', 'true');
    await user.click(screen.getByRole('radio', { name: '99%' }));
    expect(document.querySelector('output')!.textContent).toBe('99');
    expect(screen.getByRole('radio', { name: '99%' })).toHaveAttribute('aria-checked', 'true');
  });

  it('combobox filters, moves with arrows, skips disabled options, selects with Enter and returns on Change', async () => {
    const user = userEvent.setup();
    render(<ComboboxHarness />);
    const input = screen.getByRole('combobox', { name: 'Datasource' });
    await user.click(input);
    expect(screen.getByRole('listbox')).toBeVisible();
    expect(screen.getAllByRole('option')).toHaveLength(3);
    await user.type(input, 'prod');
    expect(screen.getAllByRole('option')).toHaveLength(1);
    await user.clear(input);
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowDown}');
    expect(input).toHaveAttribute('aria-activedescendant', 'ds-option-a');
    await user.keyboard('{ArrowDown}');
    expect(input).toHaveAttribute('aria-activedescendant', 'ds-option-b');
    await user.keyboard('{Enter}');
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    expect(screen.getByText('prod-prometheus-eu')).toBeVisible();
    expect(screen.getByText('Healthy')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Change' }));
    expect(screen.getByRole('combobox', { name: 'Datasource' })).toHaveFocus();
  });

  it('combobox offers the no-match action and closes on Escape', async () => {
    const user = userEvent.setup();
    const create = vi.fn();
    render(<ComboboxHarness noMatch={create} />);
    const input = screen.getByRole('combobox', { name: 'Datasource' });
    await user.type(input, 'zzz');
    expect(screen.getByText('Nothing for zzz')).toBeVisible();
    await user.keyboard('{Enter}');
    expect(create).toHaveBeenCalledWith('zzz');
    await user.type(input, 'x');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  it('search field focuses on "/" except while typing elsewhere', async () => {
    const user = userEvent.setup();
    render(<><SearchField id="q" label="Search" shortcut /><TextareaField id="d" label="Description" /></>);
    const search = screen.getByRole('searchbox', { name: 'Search' });
    await user.keyboard('/');
    expect(search).toHaveFocus();
    const description = screen.getByRole('textbox', { name: 'Description' });
    await user.click(description);
    await user.keyboard('/');
    expect(description).toHaveFocus();
    expect(description).toHaveValue('/');
  });

  it('pager exposes position, per-page choice and disabled steps', async () => {
    const user = userEvent.setup();
    const size = vi.fn();
    render(<Pager id="ps" label="Result pages" position="Page 2 · showing 11–20 of 23" pageSize={10} onPageSizeChange={size} hasPrevious hasNext={false} />);
    expect(screen.getByRole('navigation', { name: 'Result pages' })).toHaveTextContent('Page 2 · showing 11–20 of 23');
    expect(screen.getByRole('button', { name: 'Next ›' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '‹ Previous' })).toBeEnabled();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Per page' }), '20');
    expect(size).toHaveBeenCalledWith(20);
  });

  it('chips hand a cleared custom input to the owner and follow an owner-driven custom value', async () => {
    const user = userEvent.setup();
    function Owner() {
      const [value, setValue] = useState('99.9');
      return <><ChoiceChips id="t" label="Target" value={value} onChange={setValue} options={[{ value: '99.9', label: '99.9%' }]} custom={{ unit: '%', format: (v) => `${v}%` }} /><output data-testid="value">{value}</output><button type="button" onClick={() => setValue('99.8')}>reset</button></>;
    }
    render(<Owner />);
    await user.click(screen.getByRole('radio', { name: 'Custom…' }));
    await user.type(screen.getByLabelText('Custom target'), '99.7');
    expect(screen.getByTestId('value')).toHaveTextContent('99.7');
    await user.clear(screen.getByLabelText('Custom target'));
    expect(screen.getByTestId('value')).toHaveTextContent('');
    await user.click(screen.getByRole('button', { name: 'reset' }));
    expect(screen.getByLabelText('Custom target')).toHaveValue('99.8');
    expect(screen.getByRole('radio', { name: 'Custom · 99.8%' })).toHaveAttribute('aria-checked', 'true');
  });

  it('combobox skips a disabled last option on End, keeps its error when selected and focuses Change after choosing', async () => {
    const user = userEvent.setup();
    const options = [{ id: 'a', label: 'alpha' }, { id: 'b', label: 'beta' }, { id: 'c', label: 'gamma', disabled: true }];
    function Owner() {
      const [value, setValue] = useState<string | null>(null);
      return <Combobox id="p" label="Pick" value={value} onChange={setValue} options={options} error={value === 'b' ? 'beta is not allowed here' : undefined} />;
    }
    render(<Owner />);
    const input = screen.getByLabelText('Pick');
    await user.click(input);
    await user.keyboard('{End}');
    expect(input).toHaveAttribute('aria-activedescendant', 'p-option-b');
    await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Change' })).toHaveFocus();
    expect(screen.getByRole('alert')).toHaveTextContent('beta is not allowed here');
  });

  it('combobox runs the no-match action from a click reached by Tab', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Combobox id="n" label="Service" value={null} onChange={() => undefined} options={[{ id: 'a', label: 'alpha' }]} noMatch={{ text: () => 'nothing', action: { label: (term) => `Create ${term}`, onSelect } }} />);
    await user.type(screen.getByLabelText('Service'), 'zeta');
    await user.tab();
    expect(screen.getByRole('button', { name: 'Create zeta' })).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(onSelect).toHaveBeenCalledWith('zeta');
  });
});
