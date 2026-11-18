import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it } from 'vitest';
import type { components } from '../src/api/generated.ts';
import { combineMaintenanceCounts } from '../src/slos/maintenance.ts';
import { SloMaintenanceComparison } from '../src/slos/SloMaintenanceComparison.tsx';

type Current = components['schemas']['SloOperationalValue'];

afterEach(cleanup);

const example = {
  quality: {
    state: 'complete',
    excluded_intervals: 10,
    excluded_good: '98002',
    excluded_total: '100000',
  },
  performance: {
    good: '999820',
    total: '1000000',
    bad: '180',
    attainment: '0.99982',
    provisional: false,
  },
} as Current;

it('compares the same evaluation with and without maintenance events', () => {
  render(<SloMaintenanceComparison current={example} />);
  const disclosure = screen.getByText('Maintenance calculation comparison').closest('details')!;
  disclosure.open = true;
  const columns = within(disclosure).getAllByRole('columnheader').map(header => header.textContent);
  expect(columns).toEqual(['Measure', 'Without maintenance', 'Including maintenance']);
  expect(screen.getByRole('row', { name: /Total events/ })).toHaveTextContent('1,000,0001,100,000');
  expect(screen.getByRole('row', { name: /Unsuccessful/ })).toHaveTextContent('1802,178');
  expect(screen.getByRole('row', { name: /Attainment/ })).toHaveTextContent('99.982%99.802%');
});

it('leaves unknown counts blank and distinguishes a wholly excluded result', () => {
  const current: Current = {
    ...example,
    quality: { ...example.quality, state: 'excluded', excluded_good: null, excluded_total: null },
    performance: { ...example.performance, good: null, total: null, bad: null, attainment: null },
  };
  render(<SloMaintenanceComparison current={current} />);
  const disclosure = screen.getByText('Maintenance calculation comparison').closest('details')!;
  disclosure.open = true;
  expect(within(disclosure).getByText('The full excluded event population is unknown.')).toBeVisible();
  expect(screen.getByRole('row', { name: /Attainment/ })).toHaveTextContent('——');
});

it('does exact decimal addition and half-even rounding beyond binary integer precision', () => {
  const result = combineMaintenanceCounts(
    { good: '9007199254740993.000000000001', total: '9007199254740994.000000000002' },
    { good: '0.000000000009', total: '0.000000000010' },
  );
  expect(result).toEqual({
    good: '9007199254740993.000000000010',
    total: '9007199254740994.000000000012',
    bad: '1.000000000002',
    attainment: '1.000000000000',
  });
  expect(combineMaintenanceCounts({ good: '1', total: '2000000000000' }, { good: '0', total: '0' })?.attainment).toBe('0.000000000000');
  expect(combineMaintenanceCounts({ good: '3', total: '2000000000000' }, { good: '0', total: '0' })?.attainment).toBe('0.000000000002');
});
