import { expect, test } from '@playwright/test';
import { openLibrary } from './fixture';

test('browsing stays local and has no unexpected browser errors', async ({ page }) => {
  const errors: string[] = [];
  const external: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text());
  });
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.protocol.startsWith('http') && url.hostname !== '127.0.0.1') external.push(url.origin);
  });
  await openLibrary(page);
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  await expect(page.locator('img[src*="/thumbnails/"]').first()).toBeVisible();
  await page.getByRole('button', { name: 'mountain.png', exact: true }).click();
  await expect(page.getByRole('dialog').getByRole('img', { name: 'mountain.png' })).toBeVisible();
  await page.keyboard.press('Escape');
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('phone controls retain touch targets with reduced motion', async ({ page }) => {
  // Firefox can report a 44 px DOMRect as 43.9999847 px.
  const minimumTarget = 44 - 0.001;
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await openLibrary(page);
  const labels = [
    'Toggle sidebar',
    'Parent folder',
    'Edit profile',
    'More filters',
    'Ascending',
    'Detailed view',
    'Compact view',
    'Grid view',
    'Find files',
  ];
  for (const name of labels) {
    const button = page.getByRole('button', { name, exact: true });
    await expect(button).toBeVisible();
    const box = await button.boundingBox();
    expect(box?.height, name).toBeGreaterThanOrEqual(minimumTarget);
    expect(box?.width, name).toBeGreaterThanOrEqual(minimumTarget);
  }
  const selection = page
    .getByRole('checkbox', { name: 'Select readme.txt', exact: true })
    .locator('..');
  const box = await selection.boundingBox();
  expect(box?.height).toBeGreaterThanOrEqual(minimumTarget);
  expect(box?.width).toBeGreaterThanOrEqual(minimumTarget);
  expect(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(
    true,
  );
});
