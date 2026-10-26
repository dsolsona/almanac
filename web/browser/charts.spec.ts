import { expect, test } from '@playwright/test';

test('charts load on demand and render SVG with exact table paging and hover details', async ({ page }, testInfo) => {
  const chartRequests: string[] = [];
  page.on('request', (request) => { if (/echarts|components\/charts|ChartsSection|chartFixture/.test(request.url())) chartRequests.push(request.url()); });
  await page.goto('/design-system');
  await expect(page.getByRole('heading', { name: 'Clear signals. Steady decisions.' })).toBeVisible();
  expect(chartRequests).toHaveLength(0);
  await page.getByRole('button', { name: 'Charts' }).click();
  await expect(page.getByRole('heading', { name: 'Aligned charts and exact values' })).toBeVisible();
  const plot = page.locator('.timeline-chart__plot');
  await expect(plot.locator('svg')).toBeVisible();
  const visiblePointMarkers = await plot.locator('svg path[fill]:not([fill="none"])').count();
  expect(visiblePointMarkers).toBeGreaterThanOrEqual(8);
  const isolatedMinuteNineMarker = await plot.locator('svg path[fill]:not([fill="none"])').evaluateAll((paths, plotRect) => {
    const bounds = plotRect as { x: number; y: number; width: number; height: number };
    const expectedX = bounds.x + 66 + (9 / 16) * (bounds.width - 86);
    return paths.some((path) => {
      const marker = path.getBoundingClientRect();
      const centerX = marker.x + marker.width / 2;
      const centerY = marker.y + marker.height / 2;
      return marker.width > 0 && marker.height > 0 && marker.width <= 12 && marker.height <= 12 &&
        Math.abs(centerX - expectedX) <= 5 && centerY >= bounds.y + bounds.height * .05 && centerY <= bounds.y + bounds.height * .25;
    });
  }, await plot.boundingBox());
  expect(isolatedMinuteNineMarker, 'isolated complete sample at minute 9 has a visible SVG marker').toBe(true);
  await expect(page.getByRole('table').locator('tbody').getByText('99.970000 %', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('Complete', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('page 1 of 2')).toBeVisible();
  await page.locator('.timeline-chart').screenshot({ path: testInfo.outputPath('timeline-panel.png') });
  await page.locator('.exact-values').screenshot({ path: testInfo.outputPath('exact-values-page1.png') });
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('page 2 of 2')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Next' })).toBeDisabled();
  await expect.poll(() => chartRequests.length).toBeGreaterThan(0);

  await plot.scrollIntoViewIfNeeded();
  const box = await plot.boundingBox();
  if (!box) throw new Error('Chart plot has no visible bounds');
  const moveToMinute = (minute: number) => page.mouse.move(box.x + 66 + (minute / 16) * (box.width - 66 - 20), box.y + box.height * 0.18);
  await moveToMinute(4);
  const tooltip = page.locator('.timeline-chart-tooltip');
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toContainText('SLI · Example service');
  await expect(tooltip).toContainText('99.960000 % · Partial');
  await moveToMinute(5);
  await expect(tooltip).toContainText('SLI · Example service: Missing');
  await expect(tooltip).not.toContainText('99.960000 %');
  await moveToMinute(6);
  await expect(tooltip).toContainText('SLI · Example service: 0.000000 % · Complete');
  await moveToMinute(8);
  await expect(tooltip).toContainText('No sample at this timestamp');
  await expect(tooltip).not.toContainText('99.950000 %');
  await moveToMinute(10);
  await expect(tooltip).toContainText('SLI · Example service: Zero traffic');
  await page.screenshot({ path: test.info().outputPath('charts.png'), fullPage: true });
});

test('chart theme updates and keyboard pagination work without page overflow', async ({ page }, testInfo) => {
  await page.goto('/design-system');
  await page.getByRole('button', { name: 'Charts' }).click();
  const svg = page.locator('.timeline-chart__plot svg');
  await expect(svg).toBeVisible();
  const before = await svg.locator('path').first().getAttribute('stroke');
  const startedDark = await page.locator('.theme-toggle').getAttribute('aria-pressed') === 'true';
  await page.getByRole('button', { name: new RegExp(`switch to ${startedDark ? 'light' : 'dark'} theme`, 'i') }).click();
  if (startedDark) await expect(page.locator('html')).not.toHaveClass(/dark/);
  else await expect(page.locator('html')).toHaveClass(/dark/);
  await expect.poll(() => svg.locator('path').first().getAttribute('stroke')).not.toBe(before);

  const dimensions = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth }));
  expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport);
  const scrollRegion = page.getByRole('region', { name: 'Exact timeline values' });
  await scrollRegion.focus();
  await expect(scrollRegion).toBeFocused();
  const next = page.getByRole('button', { name: 'Next' });
  await next.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByText('page 2 of 2')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('charts-theme.png'), fullPage: true });
});

test('chart series colors follow the resolved semantic CSS token', async ({ page }) => {
  await page.goto('/design-system');
  await page.getByRole('button', { name: 'Charts' }).click();
  const svg = page.locator('.timeline-chart__plot svg');
  await expect(svg).toBeVisible();
  await page.evaluate(() => document.documentElement.style.setProperty('--s1', '#123456'));
  await expect.poll(() => svg.locator('path[stroke="#123456"]').count()).toBeGreaterThan(0);
});

test('chart and table retain their shared layout without catalogue styles', async ({ page }) => {
  await page.goto('/design-system');
  await expect(page.getByRole('button', { name: 'Charts' })).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.evaluate(() => document.querySelectorAll('style[data-vite-dev-id*="catalogue.css"]').forEach((style) => style.remove()));
  await page.getByRole('button', { name: 'Charts' }).click();
  const panel = page.locator('.timeline-chart');
  const plot = page.locator('.timeline-chart__plot');
  const tableScroll = page.locator('.exact-values__scroll');
  await expect(plot.locator('svg')).toBeVisible();
  await expect.poll(() => panel.evaluate((element) => getComputedStyle(element).borderTopWidth)).toBe('1px');
  expect(await plot.evaluate((element) => getComputedStyle(element).height)).toBe('420px');
  expect(await tableScroll.evaluate((element) => getComputedStyle(element).overflow)).toBe('auto');
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await plot.evaluate((element) => getComputedStyle(element).height)).toBe('400px');
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test('rendered axis labels meet text contrast in light and dark themes', async ({ page }) => {
  await page.goto('/design-system');
  await page.getByRole('button', { name: 'Charts' }).click();
  const svg = page.locator('.timeline-chart__plot svg');
  await expect(svg).toBeVisible();
  for (const dark of [false, true]) {
    await page.evaluate((useDark) => document.documentElement.classList.toggle('dark', useDark), dark);
    const contrast = await page.evaluate(() => {
      const label = [...document.querySelectorAll('.timeline-chart__plot svg text')].find((element) => element.textContent?.trim() === 'SLI (%)');
      const panel = document.querySelector('.timeline-chart');
      if (!label || !panel) return null;
      const rgb = (color: string) => (color.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
      const luminance = (color: string) => {
        const [red, green, blue] = rgb(color).map((value) => {
          const channel = value / 255;
          return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
        });
        return 0.2126 * red! + 0.7152 * green! + 0.0722 * blue!;
      };
      const foreground = luminance(getComputedStyle(label).fill);
      const background = luminance(getComputedStyle(panel).backgroundColor);
      const [lighter, darker] = [foreground, background].sort((a, b) => b - a);
      return (lighter! + 0.05) / (darker! + 0.05);
    });
    expect(contrast, `${dark ? 'dark' : 'light'} axis label contrast`).not.toBeNull();
    expect(contrast).toBeGreaterThanOrEqual(4.5);
  }
});

test('reopening charts across navigation and theme changes leaves the page error-free', async ({ page, baseURL }) => {
  const browserErrors: string[] = [];
  const authContextURL = new URL('/api/v1/auth/context', baseURL).href;
  let expectedContextFailures = 0;
  const unavailableContextRequests = new Set<import('@playwright/test').Request>();
  page.on('response', (response) => {
    if (response.url() === authContextURL && response.status() === 404) {
      unavailableContextRequests.add(response.request());
    }
  });
  page.on('pageerror', (error) => browserErrors.push(`pageerror: ${error.stack ?? error.message}`));
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    // The static catalogue server has no API. Assert its two expected root-page
    // failures explicitly while retaining every chart/other console error.
    if (message.location().url === authContextURL
      && message.text() === 'Failed to load resource: the server responded with a status of 404 (Not Found)') {
      expectedContextFailures += 1;
    } else browserErrors.push(`console: ${message.text()}`);
  });
  page.on('requestfailed', (request) => {
    // The auth client cancels an unwanted 404 body rather than retaining it.
    // Exempt only that observed response, never chart requests or unknown errors.
    if (unavailableContextRequests.has(request) && request.failure()?.errorText === 'net::ERR_ABORTED') return;
    browserErrors.push(`requestfailed: ${request.url()} ${request.failure()?.errorText ?? ''}`);
  });
  await page.goto('/design-system');
  await expect(page.getByRole('button', { name: 'Charts' })).toBeVisible();

  for (let cycle = 0; cycle < 3; cycle += 1) {
    await page.getByRole('button', { name: 'Charts' }).click();
    await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
    const themeToggle = page.locator('.theme-toggle');
    const isDark = await themeToggle.getAttribute('aria-pressed') === 'true';
    await themeToggle.click();
    if (isDark) await expect(page.locator('html')).not.toHaveClass(/dark/);
    else await expect(page.locator('html')).toHaveClass(/dark/);
    await page.getByRole('link', { name: 'Controls' }).click();
    await expect(page.getByRole('heading', { name: 'Calendar, switches and loading' })).toBeVisible();
    await page.getByRole('button', { name: 'Charts' }).click();
    await expect(page.getByRole('heading', { name: 'Aligned charts and exact values' })).toBeVisible();
    if (cycle < 2) {
      await page.getByRole('link', { name: 'ALMANAC' }).click();
      await expect(page.getByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Check again' })).toBeVisible();
      await page.goBack();
      await expect(page.getByRole('button', { name: 'Charts' })).toBeVisible();
    }
  }
  expect(browserErrors).toEqual([]);
  expect(expectedContextFailures).toBe(2);
  expect(unavailableContextRequests.size).toBe(2);
});

test('a failed lazy chart import leaves catalogue controls available and reload retries it', async ({ page }) => {
  let chartChunkRequests = 0;
  const pageErrors: string[] = [];
  page.on('pageerror', (error) => pageErrors.push(error.stack ?? error.message));
  await page.route('**/src/catalogue/ChartsSection.tsx*', async (route) => {
    chartChunkRequests += 1;
    if (chartChunkRequests === 1) await route.abort('failed');
    else await route.continue();
  });
  await page.goto('/design-system');
  await page.getByRole('button', { name: 'Charts' }).click();
  await expect(page.getByRole('alert').filter({ hasText: /chart examples could not be loaded/i })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Foundations' })).toBeVisible();
  await page.getByRole('button', { name: 'Primary' }).click();
  await expect(page.locator('.demo-feedback')).toContainText('Count: 1');

  await page.getByRole('button', { name: 'Reload catalogue to retry' }).click();
  await expect(page.getByRole('button', { name: 'Charts' })).toBeVisible();
  await page.getByRole('button', { name: 'Charts' }).click();
  await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
  expect(chartChunkRequests).toBe(2);
  expect(pageErrors).toEqual([]);
});
