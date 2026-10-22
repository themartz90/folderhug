import { expect, test, type Page } from '@playwright/test';
import { createProfile, mutationHeaders, navigation, openLibrary, removeProfile } from './fixture';

async function findMedia(page: Page) {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).click();
  const created = page.waitForResponse((response) => response.url().endsWith('/scans') && response.request().method() === 'POST');
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  const scan = await (await created).json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/scans/${scan.id}`)).json()).state).toBe('completed');
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible();
  return scan;
}

test('search shows image thumbnails, video icons and space below selection actions', async ({ page }) => {
  await openLibrary(page);
  const requested: string[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && request.url().endsWith('/thumbnails')) requested.push(...request.postDataJSON().entry_ids);
  });
  const scan = await findMedia(page);
  const mountain = page.getByRole('article', { name: 'mountain.png', exact: true });
  await mountain.scrollIntoViewIfNeeded();
  const image = mountain.locator('img[src*="/thumbnails/"]');
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((element) => (element as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  const video = page.getByRole('article', { name: 'unsupported-video.mp4', exact: true });
  await expect(video.locator('.file-mark.video svg')).toBeVisible();
  await expect(video.locator('img, video')).toHaveCount(0);
  const indexed = await (await page.request.get(`/api/v1/search?profile_id=${scan.profile_id}&directory_id=${scan.directory_id}&limit=100`)).json();
  expect(indexed.entries).toHaveLength(3);
  const imageIDs = indexed.entries.filter((entry: { category: string }) => entry.category === 'image').map((entry: { id: string }) => entry.id);
  expect(requested.every((id) => imageIDs.includes(id))).toBe(true);
  const spacing = await page.locator('.activity-actions').evaluate((actions) => actions.nextElementSibling!.getBoundingClientRect().top - actions.getBoundingClientRect().bottom);
  expect(spacing).toBeGreaterThanOrEqual(12);
  await page.screenshot({ path: '../.local/search-previews/desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: 'dark' });
  await mountain.scrollIntoViewIfNeeded();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: '../.local/search-previews/phone.png', fullPage: true });
  await mountain.getByRole('button', { name: 'mountain.png', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'mountain.png', exact: true })).toBeVisible();
  expect(requested.length).toBeGreaterThan(0);
});

test('search respects disabled previews for its profile', async ({ page }, testInfo) => {
  const csrf = await openLibrary(page);
  const profile = await createProfile(page, csrf, `No search previews ${testInfo.project.name}`);
  try {
    const response = await page.request.patch(`/api/v1/profiles/${profile.id}`, { headers: mutationHeaders(csrf), data: { previews_enabled: false } });
    expect(response.ok()).toBeTruthy();
    await page.goto(`/#page=files&profile=${profile.id}&directory=root`);
    await page.reload();
    let requests = 0;
    page.on('request', (request) => {
      if (request.method() === 'POST' && request.url().endsWith('/thumbnails')) requests++;
    });
    await findMedia(page);
    await expect(page.getByRole('article', { name: 'mountain.png', exact: true }).locator('.file-mark.image svg')).toBeVisible();
    await expect(page.locator('.job-list img')).toHaveCount(0);
    expect(requests).toBe(0);
  } finally { await removeProfile(page, csrf, profile); }
});

test('an unavailable thumbnail leaves search controls usable', async ({ page }) => {
  await openLibrary(page);
  let requests = 0;
  await page.route('**/api/v1/profiles/*/thumbnails', (route) => {
    requests++;
    return route.fulfill({ status: 503, json: { code: 'unavailable', message: 'Synthetic unavailable thumbnail.' } });
  });
  await findMedia(page);
  const mountain = page.getByRole('article', { name: 'mountain.png', exact: true });
  await mountain.scrollIntoViewIfNeeded();
  await expect.poll(() => requests).toBeGreaterThan(0);
  await expect(mountain.locator('.file-mark.image svg')).toBeVisible();
  await mountain.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await expect(page.getByText('1 selected', { exact: true })).toBeVisible();
  await expect(mountain.getByRole('button', { name: 'Download', exact: true })).toBeEnabled();
});

test('offscreen search images wait for scrolling and pending requests stop on navigation', async ({ page }) => {
  await openLibrary(page);
  const scan = await findMedia(page);
  const existing = await (await page.request.get(`/api/v1/search?profile_id=${scan.profile_id}&directory_id=${scan.directory_id}&limit=100`)).json();
  expect(existing.entries).toHaveLength(3);
  const template = existing.entries.find((entry: { category: string }) => entry.category === 'image');
  const entries = Array.from({ length: 50 }, (_, i) => ({ ...template, id: `lazy-${i}`, name: `Result ${i}.png`, category: i === 49 ? 'image' : 'document' }));
  let requests = 0;
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let observed!: () => void;
  const started = new Promise<void>((resolve) => { observed = resolve; });
  await page.route('**/api/v1/search?*', (route) => route.fulfill({ json: { ...existing, entries, next_cursor: null } }));
  await page.route('**/api/v1/profiles/*/thumbnails', async (route) => {
    if (!route.request().postDataJSON().entry_ids.includes('lazy-49')) return route.continue();
    requests++;
    observed();
    await held;
    await route.fulfill({ json: { thumbnails: [] } }).catch(() => {});
  });
  try {
    await page.getByRole('button', { name: 'Show results', exact: true }).click();
    const last = page.getByRole('article', { name: 'Result 49.png', exact: true });
    await expect(last).toBeAttached();
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    expect(requests).toBe(0);
    await last.scrollIntoViewIfNeeded();
    await started;
    expect(requests).toBe(1);
    const cancelled = page.waitForEvent('requestfailed', { predicate: (request) => request.method() === 'POST' && request.url().endsWith('/thumbnails') && request.postDataJSON().entry_ids.includes('lazy-49') });
    await navigation(page, 'Overview');
    await cancelled;
  } finally { release(); }
});
