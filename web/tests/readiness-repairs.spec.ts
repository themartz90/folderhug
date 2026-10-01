import { expect, test } from '@playwright/test';
import { openLibrary } from './fixture';

test.beforeEach(async ({ page }, testInfo) => {
  if (testInfo.title === 'an older scan response cannot replace a refreshed terminal state') {
    // Keep response ordering controlled by explicit refreshes rather than scan events.
    await page.route('**/api/v1/events', (route) => route.fulfill({ status: 204 }));
  }
  await openLibrary(page);
});

test('scan limits can be cleared and re-entered without breaking the dialog', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Find files', exact: true });
  await dialog.getByText('Scan limits and exclusions', { exact: true }).click();
  await dialog.getByLabel('Entry limit', { exact: true }).fill('');
  await expect(dialog.getByRole('button', { name: 'Find files', exact: true })).toBeVisible();
  await dialog.getByLabel('Entry limit', { exact: true }).fill('500');
  await expect(dialog).toContainText('500 entries');
  expect(errors).toEqual([]);
});

test('theme changes apply button foreground and background together', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'no-preference' });
  const button = page.getByRole('button', { name: 'Find files', exact: true });
  await expect(page.locator('.app')).toHaveAttribute('data-theme', 'light');
  await expect(button).toHaveCSS('background-color', 'rgb(255, 255, 255)');
  const colors = await button.evaluate((element) => {
    element.closest('.app')!.setAttribute('data-theme', 'dark');
    const style = getComputedStyle(element);
    return { foreground: style.color, background: style.backgroundColor };
  });
  expect(colors).toEqual({ foreground: 'rgb(236, 236, 240)', background: 'rgb(33, 34, 38)' });
});

test('result pagination cannot skip pages while the next page loads', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible();
  const response = await page.request.get('/api/v1/search?limit=100');
  const existing = await response.json();
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/v1/search?*', async (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor) await held;
    await route.fulfill({ json: { ...existing, next_cursor: cursor ? null : 'synthetic-next-page' } });
  });
  try {
    await page.getByRole('button', { name: 'Show results', exact: true }).click();
    const next = page.getByRole('button', { name: 'Next page', exact: true });
    await expect(next).toBeEnabled();
    await next.click();
    await expect(page.getByText('Loading results…', { exact: true })).toBeVisible();
    await expect(next).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Previous page', exact: true })).toBeDisabled();
    release();
    await expect(page.getByText('Loading results…', { exact: true })).toBeHidden();
    await expect(page.locator('.activity-pagination')).toContainText('Page 2');
    await page.getByRole('button', { name: 'Previous page', exact: true }).click();
    await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  } finally { release(); }
});

test('an older scan response cannot replace a refreshed terminal state', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  const created = page.waitForResponse((response) => response.url().endsWith('/scans') && response.request().method() === 'POST');
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  const scan = await (await created).json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/scans/${scan.id}`)).json()).state).toBe('completed');
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible();
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let received!: () => void;
  const firstRequest = new Promise<void>((resolve) => { received = resolve; });
  let delivered!: () => void;
  const firstDelivered = new Promise<void>((resolve) => { delivered = resolve; });
  let requests = 0;
  let heldRequest: import('@playwright/test').Request;
  await page.route('**/api/v1/scans/*', async (route) => {
    const response = await route.fetch();
    const scan = await response.json();
    requests++;
    if (requests === 1) {
      heldRequest = route.request();
      received();
      await held;
      await route.fulfill({ json: { ...scan, state: 'running' } }).catch(() => {});
      delivered();
    } else await route.fulfill({ json: { ...scan, state: 'cancelled' } });
  });
  try {
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await firstRequest;
    const cancelled = page.waitForEvent('requestfailed', { predicate: (request) => request === heldRequest });
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await cancelled;
    await expect.poll(() => requests).toBe(2);
    await expect(page.getByText('Stopped finding files', { exact: true })).toBeVisible();
    release();
    await firstDelivered;
    await expect(page.getByText('Stopped finding files', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Close results', exact: true })).toBeEnabled();
  } finally { release(); }
});
