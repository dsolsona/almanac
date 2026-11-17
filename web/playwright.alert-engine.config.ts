import {defineConfig, devices} from '@playwright/test';

// Runs only against the opt-in acceptance binary in the isolated e2e stack.
export default defineConfig({
  testDir: '.', outputDir: './test-results/alert-engine', retries: 0, workers: 1,
  timeout: 60_000, expect: {timeout: 10_000}, reporter: [['list']],
  use: {baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18188', ignoreHTTPSErrors: true,
    trace: 'retain-on-failure', ...devices['Desktop Chrome']},
  projects: [
    {name: 'onboarding', testMatch: 'e2e/onboarding.spec.ts'},
    {name: 'engine', testMatch: 'perf/alert-engine.perf.ts', dependencies: ['onboarding'],
      use: {storageState: 'e2e/.auth/admin.json'}},
  ],
});
