import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { mutationHeaders, navigation, openLibrary } from './fixture';

test('folder results preserve selection on refresh and can be dismissed', async ({ page }, testInfo) => {
  await openLibrary(page);
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible();
  await expect(page.locator('.find-status')).toContainText('Media');
  await expect(page.locator('.sidebar .find-return')).toHaveCount(0);
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [1440, 1024, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await expect(page.getByRole('button', { name: 'Close results', exact: true })).toBeInViewport();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      if (testInfo.project.name === 'chromium') await page.screenshot({ path: testInfo.outputPath(`results-${colorScheme}-${width}.png`) });
    }
  }
  const selection = page.getByRole('checkbox', { name: 'Select mountain.png', exact: true });
  await selection.check();
  await page.getByLabel('Filter results by name').fill('not submitted');
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/v1/search?*', async (route) => {
    const response = await route.fetch();
    await held;
    await route.fulfill({ response });
  });
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByText('Loading results…', { exact: true })).toBeVisible();
  release();
  await expect(page.getByText('Loading results…', { exact: true })).toBeHidden();
  await expect(selection).toBeChecked();
  await page.getByRole('button', { name: 'Show results', exact: true }).click();
  await expect(page.getByText('No files match. Try another type or name.', { exact: true })).toBeVisible();
  await navigation(page, 'Overview');
  await page.getByRole('button', { name: 'Return to results: Media', exact: true }).click();
  await expect(page.locator('.find-status')).toContainText('Media');
  await page.getByRole('button', { name: 'Close results', exact: true }).click();
  await expect(page.locator('.find-status')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Media', exact: true })).toBeVisible();
  await page.reload();
  await expect(page.locator('.find-status')).toHaveCount(0);
});

for (const missing of ['folder', 'mount']) {
  test(`a favorite can be removed when its ${missing} is unavailable`, async ({ page }) => {
    const csrf = await openLibrary(page);
    const prefs = await (await page.request.get('/api/v1/preferences')).json();
    await page.request.put('/api/v1/preferences', { headers: mutationHeaders(csrf), data: {
      ...prefs, last_destination: { destination_id: 'media', path: 'Movies' },
      destination_favorites: [{ destination_id: 'media', path: 'Movies' }],
    } });
    await page.route('**/api/v1/destinations', async (route) => route.fulfill({ json: { destinations: missing === 'mount' ? [] : [{ id: 'media', label: 'Media', available: true, available_bytes: null, browseable: true }] } }));
    await page.route('**/api/v1/destinations/media/folders*', async (route) => route.fulfill({ status: 404, json: { code: 'not_found', message: 'Folder unavailable.' } }));
    await page.reload();
    await page.getByRole('checkbox', { name: 'Select readme.txt', exact: true }).check();
    await page.getByRole('button', { name: 'Save to server', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: `Remove favorite ${missing === 'mount' ? 'media' : 'Media'} / Movies`, exact: true }).click();
    await expect(dialog.getByText('Favorite locations', { exact: true })).toHaveCount(0);
    expect((await (await page.request.get('/api/v1/preferences')).json()).destination_favorites ?? []).toEqual([]);
  });
}
