import { expect, test, type Page } from '@playwright/test';
import { openLibrary } from './fixture';

async function prepareResults(page: Page) {
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
  const source = await (await page.request.get(`/api/v1/search?profile_id=${scan.profile_id}`)).json();
  const image = source.entries.find((entry: { name: string }) => entry.name === 'mountain.png');
  const entries = Array.from({ length: 60 }, (_, i) => i === 40 ? image : {
    ...image, id: `return-${i}`, name: `Result ${i}.txt`, category: 'document',
  });
  const state = { expired: false };
  await page.route('**/api/v1/search?*', (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor && state.expired) return route.fulfill({ status: 409, json: { code: 'snapshot_expired', message: 'Synthetic expired search.' } });
    return route.fulfill({ json: { ...source, entries, next_cursor: cursor ? null : 'return-page-2' } });
  });
  await page.getByRole('button', { name: 'Show results', exact: true }).click();
  await expect(page.locator('.job-list article')).toHaveCount(60);
  return state;
}

async function openImage(page: Page) {
  await expect(page.getByText('Loading results…', { exact: true })).toBeHidden();
  const result = page.getByRole('article', { name: 'mountain.png', exact: true });
  await result.scrollIntoViewIfNeeded();
  const scroll = await page.getByRole('region', { name: 'Search results', exact: true }).evaluate((element) => element.scrollTop);
  expect(scroll).toBeGreaterThan(1000);
  await result.getByRole('button', { name: 'mountain.png', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'mountain.png', exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();
  return scroll;
}

for (const view of [
  { name: 'wide', width: 2560, height: 1440 },
  { name: 'medium', width: 1440, height: 1000 },
  { name: 'phone', width: 390, height: 844 },
]) {
  test(`browser Back restores search scroll in the ${view.name} view`, async ({ page }) => {
    await page.setViewportSize(view);
    await prepareResults(page);
    const scroll = await openImage(page);
    await page.goBack();
    await expect(page.locator('.job-list article')).toHaveCount(60);
    const results = page.getByRole('region', { name: 'Search results', exact: true });
    await expect.poll(async () => Math.abs(await results.evaluate((element) => element.scrollTop) - scroll)).toBeLessThan(3);
  });

  test(`Return to results restores page and scroll in the ${view.name} view`, async ({ page }) => {
    await page.setViewportSize(view);
    await prepareResults(page);
    await page.getByRole('button', { name: 'Next page', exact: true }).click();
    await expect(page.locator('.activity-pagination')).toContainText('Page 2');
    const scroll = await openImage(page);
    await page.locator('.find-return').click();
    await expect(page.locator('.activity-pagination')).toContainText('Page 2');
    const results = page.getByRole('region', { name: 'Search results', exact: true });
    await expect.poll(async () => Math.abs(await results.evaluate((element) => element.scrollTop) - scroll)).toBeLessThan(3);
    await page.getByLabel('Filter results by name').fill('mountain');
    await page.getByRole('button', { name: 'Show results', exact: true }).click();
    await expect(page.locator('.activity-pagination')).toContainText('Page 1');
    await expect.poll(() => results.evaluate((element) => element.scrollTop)).toBe(0);
  });
}

test('an expired return cursor starts at the top of the first page', async ({ page }) => {
  const state = await prepareResults(page);
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await expect(page.locator('.activity-pagination')).toContainText('Page 2');
  await openImage(page);
  state.expired = true;
  await page.locator('.find-return').click();
  await expect(page.getByText('Search results expired. Showing page 1 again.', { exact: true })).toBeVisible();
  await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  await expect.poll(() => page.getByRole('region', { name: 'Search results', exact: true }).evaluate((element) => element.scrollTop)).toBe(0);
});

test('a new scan starts without the previous search page or scroll', async ({ page }) => {
  await prepareResults(page);
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await expect(page.getByText('Loading results…', { exact: true })).toBeHidden();
  const result = page.getByRole('article', { name: 'mountain.png', exact: true });
  await result.scrollIntoViewIfNeeded();
  await result.getByRole('button', { name: 'Open parent folder', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  const created = page.waitForResponse((response) => response.url().endsWith('/scans') && response.request().method() === 'POST');
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await created;
  await expect(page.locator('.job-list article')).toHaveCount(60);
  await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  await expect.poll(() => page.getByRole('region', { name: 'Search results', exact: true }).evaluate((element) => element.scrollTop)).toBe(0);
});
