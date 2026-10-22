import { expect, test } from '@playwright/test';
import { openLibrary } from './fixture';

const views = [
  { name: 'wide', width: 2560, height: 1440, columns: 3 },
  { name: 'medium', width: 1440, height: 1000, columns: 2 },
  { name: 'phone', width: 390, height: 844, columns: 1 },
];

for (const view of views) {
  test(`search layout ${view.name} keeps results readable and controls usable`, async ({ page }) => {
    await page.setViewportSize({ width: view.width, height: view.height });
    await page.emulateMedia({ colorScheme: view.name === 'medium' ? 'light' : 'dark' });
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
    const existing = await (await page.request.get(`/api/v1/search?profile_id=${scan.profile_id}`)).json();
    const original = existing.entries;
    const longName = 'A very long filename with spaces and Unicode café 雪 ' + 'filename'.repeat(18) + '.txt';
    const entries = [...original, ...Array.from({ length: 3 }, (_, i) => ({
      ...original[0], id: `layout-${i}`, name: i === 0 ? longName : `Result ${i + 4}.txt`,
      category: 'document', escaped_path: `/Synthetic/${'long-folder-name/'.repeat(i === 0 ? 10 : 1)}result-${i}.txt`,
    }))];
    await page.route('**/api/v1/search?*', (route) => {
      const cursor = new URL(route.request().url()).searchParams.get('cursor');
      return route.fulfill({ json: { ...existing, entries: cursor ? [{ ...entries[3], id: 'next-result', name: 'Next page result.txt' }] : entries, next_cursor: cursor ? null : 'layout-next' } });
    });
    await page.getByRole('button', { name: 'Show results', exact: true }).click();
    const cards = page.locator('.job-list article');
    await expect(cards).toHaveCount(6);
    const bounds = await cards.evaluateAll((items) => items.map((item) => {
      const rect = item.getBoundingClientRect();
      return { x: rect.x, y: rect.y, width: rect.width, right: rect.right };
    }));
    for (let i = 1; i < view.columns; i++) {
      expect(Math.abs(bounds[i]!.y - bounds[0]!.y)).toBeLessThan(2);
      expect(bounds[i]!.x).toBeGreaterThan(bounds[i - 1]!.right);
    }
    expect(bounds[view.columns]!.y).toBeGreaterThan(bounds[0]!.y);
    expect(Math.abs(bounds[view.columns]!.x - bounds[0]!.x)).toBeLessThan(2);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(await page.locator('.job-list').evaluate((list) => list.scrollWidth <= list.clientWidth)).toBe(true);
    expect(await cards.evaluateAll((items) => items.every((item) => item.scrollWidth <= item.clientWidth))).toBe(true);
    await expect(page.getByRole('button', { name: longName, exact: true })).toBeVisible();
    await cards.first().scrollIntoViewIfNeeded();
    const image = cards.first().locator('img[src*="/thumbnails/"]');
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((element) => (element as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
    await page.screenshot({ path: `../.local/search-layout/${view.name}.png`, fullPage: true });
    if (view.name === 'phone') {
      await page.getByRole('article', { name: longName, exact: true }).scrollIntoViewIfNeeded();
      await page.screenshot({ path: '../.local/search-layout/phone-long-name.png', fullPage: true });
    }

    await cards.first().getByRole('checkbox').check();
    await cards.nth(2).getByRole('checkbox').check();
    await expect(page.getByText('2 selected', { exact: true })).toBeVisible();
    await page.getByRole('checkbox', { name: 'Select files on this page', exact: true }).check();
    await expect(page.getByText('6 selected', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Clear selection', exact: true }).click();
    await expect(page.getByText('0 selected', { exact: true })).toBeVisible();
    const open = cards.first().getByRole('button', { name: original[0].name, exact: true });
    await open.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: original[0].name, exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toBeHidden();
    await page.locator('.find-return').click();
    await expect(cards).toHaveCount(6);
    await page.getByRole('button', { name: 'Next page', exact: true }).click();
    await expect(page.locator('.activity-pagination')).toContainText('Page 2');
    await expect(page.getByRole('article', { name: 'Next page result.txt', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Previous page', exact: true }).click();
    await expect(cards).toHaveCount(6);
    await expect(page.locator('.activity-pagination')).toContainText('Page 1');
  });
}
