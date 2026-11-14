import { defineConfig, devices } from '@playwright/test';

// F4-T02 only; real isolated Compose stack, no engine or production calculator change.
export default defineConfig({
  testDir: '.', outputDir: './test-results/alert-calculation',
  timeout: 60_000, expect: { timeout: 10_000 }, retries: 0, workers: 1,
  reporter: [['list'], ['html', { outputFolder: '../.local/alert-calculation/report', open: 'never' }]],
  use: { baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18188',
    ignoreHTTPSErrors: true, trace: 'retain-on-failure', ...devices['Desktop Chrome'] },
  projects: [
    { name: 'onboarding', testMatch: 'e2e/onboarding.spec.ts' },
    { name: 'seed', testMatch: 'perf/alert-calculation.seed.ts', dependencies: ['onboarding'], use: { storageState: 'e2e/.auth/admin.json' } },
    { name: 'calculation', testMatch: 'perf/alert-calculation.perf.ts', dependencies: ['seed'], use: { storageState: 'e2e/.auth/admin.json' } },
  ],
});
