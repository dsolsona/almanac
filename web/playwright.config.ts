import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './browser',
  fullyParallel: true,
  workers: 2,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:5198',
    browserName: 'chromium',
    screenshot: 'off',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'desktop-light', use: { viewport: { width: 1440, height: 1000 }, colorScheme: 'light' } },
    { name: 'desktop-dark', use: { viewport: { width: 1440, height: 1000 }, colorScheme: 'dark' } },
    { name: 'intermediate-light', use: { viewport: { width: 1024, height: 900 }, colorScheme: 'light' } },
    { name: 'intermediate-dark', use: { viewport: { width: 1024, height: 900 }, colorScheme: 'dark' } },
    { name: 'mobile-light', use: { viewport: { width: 360, height: 800 }, colorScheme: 'light' } },
    { name: 'mobile-dark', use: { viewport: { width: 360, height: 800 }, colorScheme: 'dark' } },
  ],
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port 5198 --strictPort',
    url: 'http://127.0.0.1:5198/design-system',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
