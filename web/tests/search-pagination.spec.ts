import { expect, test, type Page } from '@playwright/test';
import { openLibrary } from './fixture';

async function openResults(page: Page) {
  await page.route('**/api/v1/events', (route) => route.fulfill({ status: 204 }));
  await openLibrary(page);
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  const created = page.waitForResponse((response) => response.url().endsWith('/scans') && response.request().method() === 'POST');
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  const scan = await (await created).json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/scans/${scan.id}`)).json()).state).toBe('completed');
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible();
  const response = await page.request.get(`/api/v1/search?profile_id=${scan.profile_id}&directory_id=${scan.directory_id}&categories=video,image,audio,document,archive,other&limit=100`);
  expect(response.ok()).toBeTruthy();
  const existing = await response.json();
  expect(existing.entries).toHaveLength(3);
  return existing;
}

test('later search pages keep their results and selection during background refreshes', async ({ page }) => {
  const existing = await openResults(page);
  let secondPageRequests = 0;
  let refreshed = false;
  await page.route('**/api/v1/search?*', (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor) secondPageRequests++;
    const name = cursor ? (secondPageRequests === 1 ? 'Later file.mp4' : 'Changed file.mp4') : (refreshed ? 'New file.mp4' : 'First file.mp4');
    return route.fulfill({ json: { ...existing, entries: [{ ...existing.entries[0], id: cursor ? 'later' : 'first', name }], next_cursor: cursor ? null : 'next-page' } });
  });
  await page.getByRole('button', { name: 'Show results', exact: true }).click();
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await expect(page.locator('.activity-pagination')).toContainText('Page 2');
  await page.getByRole('checkbox', { name: 'Select Later file.mp4', exact: true }).check();

  const session = page.waitForResponse((response) => response.url().endsWith('/api/v1/auth/session'));
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await session;
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  await expect(page.getByRole('checkbox', { name: 'Select Later file.mp4', exact: true })).toBeChecked();
  expect(secondPageRequests).toBe(1);
  await expect(page.getByText('New files may appear on earlier pages. Refresh results to start again.', { exact: true })).toBeVisible();
  await page.screenshot({ path: '../.local/search-pagination/later-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.getByRole('button', { name: 'Refresh results', exact: true }).scrollIntoViewIfNeeded();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: '../.local/search-pagination/later-phone.png', fullPage: true });

  refreshed = true;
  await page.getByRole('button', { name: 'Refresh results', exact: true }).click();
  await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  await expect(page.getByRole('button', { name: 'New file.mp4', exact: true })).toBeVisible();
  await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Previous page', exact: true })).toBeDisabled();
});

test('expired search cursors explain the restart and recover on the first page', async ({ page }) => {
  const existing = await openResults(page);
  let firstPageRequests = 0;
  let expiredRequests = 0;
  await page.route('**/api/v1/search?*', (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor) {
      expiredRequests++;
      return route.fulfill({ status: 409, json: { code: 'snapshot_expired', message: 'Search expired.' } });
    }
    firstPageRequests++;
    return route.fulfill({ json: { ...existing, next_cursor: 'expired-page' } });
  });
  await page.getByRole('button', { name: 'Show results', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Next page', exact: true })).toBeEnabled();
  const beforeNext = firstPageRequests;
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await expect(page.getByText('Search results expired. Showing page 1 again.', { exact: true })).toBeVisible();
  await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  await expect(page.getByRole('button', { name: 'Next page', exact: true })).toBeEnabled();
  expect(firstPageRequests).toBe(beforeNext + 1);
  expect(expiredRequests).toBe(1);
  await page.screenshot({ path: '../.local/search-pagination/recovered.png', fullPage: true });
});
