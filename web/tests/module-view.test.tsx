import { Component, type ReactNode } from 'react';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { ModuleView } from '../src/components/ModuleView.tsx';

afterEach(cleanup);

it('reveals resolved code without a timer and ignores superseded imports', async () => {
  let first!: (module: { default: typeof View }) => void;
  let second!: (module: { default: typeof View }) => void;
  function View({ name }: { name: string }) { return <p>{name}</p>; }
  const one = vi.fn(() => new Promise<{ default: typeof View }>(resolve => { first = resolve; }));
  const two = vi.fn(() => new Promise<{ default: typeof View }>(resolve => { second = resolve; }));
  const result = render(<ModuleView load={one} componentProps={{ name: 'first' }} fallback={<p>Opening page</p>} />);
  result.rerender(<ModuleView load={two} componentProps={{ name: 'current' }} fallback={<p>Opening page</p>} />);
  await act(async () => { first({ default: View }); });
  expect(screen.getByText('Opening page')).toBeVisible();
  await act(async () => { second({ default: View }); });
  expect(screen.getByText('current')).toBeVisible();
  result.rerender(<ModuleView load={two} componentProps={{ name: 'updated' }} fallback={<p>Opening page</p>} />);
  expect(screen.getByText('updated')).toBeVisible();
  expect(two).toHaveBeenCalledOnce();
});

it('renders a previously loaded module immediately on a new mount', async () => {
  function View({ name }: { name: string }) { return <p>{name}</p>; }
  const load = vi.fn(async () => ({ default: View }));
  const first = render(<ModuleView load={load} componentProps={{ name: 'first' }} fallback={<p>Opening page</p>} />);
  expect(await screen.findByText('first')).toBeVisible();
  first.unmount();
  render(<ModuleView load={load} componentProps={{ name: 'second' }} fallback={<p>Opening page</p>} />);
  expect(screen.getByText('second')).toBeVisible();
  expect(load).toHaveBeenCalledOnce();
});

it('preserves the surrounding screen error boundary for failed imports', async () => {
  class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
    state = { failed: false };
    static getDerivedStateFromError() { return { failed: true }; }
    render() { return this.state.failed ? <p role="alert">Screen unavailable</p> : this.props.children; }
  }
  const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  const load = () => Promise.reject(new Error('module fetch failed'));
  try {
    await act(async () => { render(<Boundary><ModuleView load={load} componentProps={{}} fallback={<p>Opening page</p>} /></Boundary>); });
    expect(screen.getByRole('alert')).toHaveTextContent('Screen unavailable');
  } finally { errors.mockRestore(); }
});
