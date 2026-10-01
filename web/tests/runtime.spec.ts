import { expect, test } from '@playwright/test';
import { openLibrary } from './fixture';
import type { Entry } from '../src/lib/api';

test('grid requests image thumbnails and opens video with native controls', async ({ page }) => {
  await openLibrary(page);
  const listing = page.waitForResponse((response) => new URL(response.url()).pathname.endsWith('/browse'));
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  const { entries } = await (await listing).json() as { entries: Entry[] };
  const images = new Set(entries.filter((entry) => entry.category === 'image').map((entry) => entry.id));
  const video = entries.find((entry) => entry.name === 'unsupported-video.mp4')!;
  const batches: string[][] = [];
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.endsWith('/thumbnails') && request.method() === 'POST') {
      batches.push((request.postDataJSON() as { entry_ids: string[] }).entry_ids);
    }
  });
  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true }).locator('img')).toBeVisible();
  expect(batches.length).toBeGreaterThan(0);
  expect(batches.flat().every((id) => images.has(id))).toBe(true);
  const tile = page.getByRole('button', { name: video.name, exact: true });
  await expect(tile.locator('img')).toHaveCount(0);

  let releaseContent!: () => void;
  const holdContent = new Promise<void>((resolve) => { releaseContent = resolve; });
  const contentRoute = `**/entries/${video.id}/content?*`;
  await page.route(contentRoute, async (route) => {
    await holdContent;
    await route.abort();
  });
  await page.evaluate(() => {
    // A browser may reject this synthetic video before Playwright reads its attributes.
    const observer = new MutationObserver(() => {
      const player = document.querySelector<HTMLVideoElement>('.media-preview video');
      if (!player) return;
      const state = window as typeof window & { videoPreview?: Record<string, unknown> };
      state.videoPreview = {
        controls: player.controls,
        preload: player.preload,
        src: player.getAttribute('src'),
        poster: player.getAttribute('poster'),
      };
      observer.disconnect();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  });
  try {
    await tile.click();
    const dialog = page.getByRole('dialog', { name: video.name, exact: true });
    await expect.poll(() => page.evaluate(() =>
      (window as typeof window & { videoPreview?: Record<string, unknown> }).videoPreview,
    )).toEqual({
      controls: true,
      preload: 'metadata',
      src: `/api/v1/profiles/${video.profile_id}/entries/${video.id}/content?disposition=preview`,
      poster: null,
    });
    await expect(dialog.getByRole('button', { name: 'Download', exact: true })).toBeVisible();
    releaseContent();
    await expect(dialog.getByText('Preview unavailable. You can download the file to open it.', { exact: true })).toBeVisible();
    expect(batches.flat().every((id) => images.has(id))).toBe(true);
  } finally {
    releaseContent();
    await page.unroute(contentRoute);
  }
});

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
