import { defineConfig, devices } from '@playwright/test';

// End-to-end suite (ADR-0033) against the real Compose stack started by scripts/e2e.sh.
// Onboarding runs first through the UI and saves the administrator session; the journeys
// reuse it in parallel; the restart check runs last because it restarts the application.
const baseURL = process.env.E2E_BASE_URL ?? 'https://localhost:18188';

export default defineConfig({
  testDir: './e2e',
  // Results go to the dev container's test-results volume: trace recording on the macOS bind mount
  // fails with EACCES (F6-T07). The HTML report, written once at the end, stays in .local for reading.
  outputDir: './test-results/e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  // A failure is a failure: no retries, so flaky tests are visible and get fixed.
  retries: 0,
  workers: 4,
  reporter: [['list'], ['html', { outputFolder: '../.local/e2e/report', open: 'never' }]],
  use: {
    baseURL,
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    ...devices['Desktop Chrome'],
  },
  projects: [
    { name: 'onboarding', testMatch: 'onboarding.spec.ts' },
    { name: 'journeys', testIgnore: ['onboarding.spec.ts', 'restart.spec.ts'], dependencies: ['onboarding'], use: { storageState: 'e2e/.auth/admin.json' } },
    { name: 'restart', testMatch: 'restart.spec.ts', dependencies: ['journeys'], use: { storageState: 'e2e/.auth/admin.json' } },
  ],
});
