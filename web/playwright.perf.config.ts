import { defineConfig, devices } from '@playwright/test';

const smoke = process.env.E2E_SMOKE === '1';

// Performance suite (ADR-0033): onboarding, then the seeded fleet, then page timings, one worker so
// measurements do not compete. Run with `make test-perf`; results land in .local/perf/results.json.
export default defineConfig({
  testDir: '.',
  outputDir: smoke ? './test-results/smoke' : './test-results/perf',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18188',
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    ...devices['Desktop Chrome'],
  },
  projects: [
    { name: 'onboarding', testMatch: 'e2e/onboarding.spec.ts' },
    { name: 'seed', testMatch: 'perf/seed.setup.ts', dependencies: ['onboarding'], use: { storageState: 'e2e/.auth/admin.json' } },
    {
      name: 'pages',
      testMatch: 'perf/pages.perf.ts',
      grep: smoke ? /@smoke/ : undefined,
      dependencies: ['seed'],
      use: { storageState: 'e2e/.auth/admin.json' },
    },
  ],
});
