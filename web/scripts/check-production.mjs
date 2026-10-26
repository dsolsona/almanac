import assert from 'node:assert/strict';
import { readdir, readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';

const output = new URL('../dist/', import.meta.url);
const root = output.pathname;
async function filesIn(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(entries.map((entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? filesIn(path) : [path];
  }));
  return nested.flat();
}

assert.equal((await stat(root)).isDirectory(), true, 'production output must exist');
const allFiles = await filesIn(root);
const assets = allFiles.filter((path) => /\.(?:js|css|html)$/.test(path));
assert.ok(assets.some((path) => path.endsWith('.js')), 'production JavaScript must be emitted');
const bundles = (await Promise.all(assets.map((path) => readFile(path, 'utf8')))).join('\n');
for (const marker of ['/design-system', 'Clear signals.', 'LOCAL COMPONENT CATALOGUE', 'catalogue/Catalogue', 'catalogue.css', 'Example input', 'No SLOs are available']) {
  assert.equal(bundles.includes(marker), false, `catalogue content leaked into production output: ${marker}`);
}
assert.match(bundles, /Unable to check your session/, 'production root should show the unavailable state without an API');
assert.ok(allFiles.some((path) => path.endsWith('/fonts/Bevan-Regular.ttf')), 'production heading font is required');
assert.ok(allFiles.some((path) => path.endsWith('/fonts/Bevan-OFL.txt')), 'production font license is required');

const server = await preview({ preview: { host: '127.0.0.1', port: 5199, strictPort: true } });
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.goto('http://127.0.0.1:5199/');
  await expect(page.getByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Check again' })).toBeVisible();
  await expect(page.getByText(/Clear signals/)).toHaveCount(0);

  for (const path of ['/unrecognized-path', '/design-system', '/design-system?catalogue=true']) {
    await page.goto(`http://127.0.0.1:5199${path}`);
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await expect(page.getByText(/Clear signals/)).toHaveCount(0);
  }
  await page.goto('http://127.0.0.1:5199/?catalogue=true');
  await expect(page.getByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
  await expect(page.getByText(/Clear signals/)).toHaveCount(0);
} finally {
  await browser.close();
  await new Promise((resolve, reject) => server.httpServer.close((error) => error ? reject(error) : resolve(undefined)));
}
console.log(`Production exclusion passed across ${assets.length} emitted JS/CSS/HTML files; root, unknown, catalogue and query paths behave correctly; font and license are present.`);
