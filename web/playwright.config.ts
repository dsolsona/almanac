import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './browser',
  fullyParallel: true,
  workers: 2,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:5200',
    browserName: 'chromium',
    screenshot: 'off',
    trace: 'retain-on-failure',
  },
  // Tests that depend on theme or viewport carry the @matrix tag and run in all six setups; the rest
  // test behaviour that does not, so they run once, in desktop-light (ADR-0033).
  projects: [
    { name: 'desktop-light', use: { viewport: { width: 1440, height: 1000 }, colorScheme: 'light' } },
    { name: 'desktop-dark', grep: /@matrix/, use: { viewport: { width: 1440, height: 1000 }, colorScheme: 'dark' } },
    { name: 'intermediate-light', grep: /@matrix/, use: { viewport: { width: 1024, height: 900 }, colorScheme: 'light' } },
    { name: 'intermediate-dark', grep: /@matrix/, use: { viewport: { width: 1024, height: 900 }, colorScheme: 'dark' } },
    { name: 'mobile-light', grep: /@matrix/, use: { viewport: { width: 360, height: 800 }, colorScheme: 'light' } },
    { name: 'mobile-dark', grep: /@matrix/, use: { viewport: { width: 360, height: 800 }, colorScheme: 'dark' } },
  ],
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port 5200 --strictPort',
    url: 'http://127.0.0.1:5200/design-system',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
