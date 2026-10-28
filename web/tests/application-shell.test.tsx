import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it } from 'vitest';
import { ApplicationShell } from '../src/components/ui/ApplicationShell.tsx';

afterEach(() => { cleanup(); history.replaceState({}, '', '/'); });

it('preserves explicit agent detail links and clears selection through the sidebar list link', async () => {
  history.replaceState({}, '', '/slis');
  render(<ApplicationShell login="owner" role="administrator" pending={false} showCheckSession={false} onCheckSession={() => {}} onSignOut={() => {}}>
    <a href="/agents?agent=1234" data-app-route>Local agent</a>
  </ApplicationShell>);
  const user = userEvent.setup();
  await user.click(screen.getByRole('link', { name: 'Local agent' }));
  expect(location.pathname + location.search).toBe('/agents?agent=1234');
  await user.click(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'Agents' }));
  expect(location.pathname + location.search).toBe('/agents');
});
