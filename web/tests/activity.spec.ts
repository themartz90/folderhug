import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { createProfile, mutationHeaders, navigation, openLibrary, removeProfile, sourceState } from './fixture';

test.beforeEach(async ({ page }) => { await openLibrary(page); });

async function csrfToken(page: Page): Promise<string> {
  const response = await page.request.get('/api/v1/auth/session');
  expect(response.status()).toBe(200);
  return (await response.json() as { csrf_token: string }).csrf_token;
}

test('find files stays with progress and results', async ({ page }, testInfo) => {
  const csrf = await csrfToken(page);
  const profile = await createProfile(page, csrf, `Scan fixture ${testInfo.project.name}`);
  try {
    await page.goto(`/#page=files&profile=${profile.id}&directory=root`);
    await page.reload();
    await page.getByRole('button', { name: 'Scan', exact: true }).click();
    await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
    const dialog = page.getByRole('dialog', { name: 'Find files', exact: true });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await dialog.getByRole('button', { name: 'Find files', exact: true }).click();
    await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible({ timeout: 15_000 });
    await page.getByLabel('Filter results by name').fill('report');
    await page.getByRole('button', { name: 'Show results', exact: true }).click();
    const result = page.getByRole('article', { name: 'report.txt', exact: true });
    await expect(result).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.getByRole('button', { name: 'Back to folder', exact: true }).click();
    await page.locator('.find-return').click();
    await expect(result).toBeVisible();
    await result.getByRole('button', { name: 'Open parent folder', exact: true }).click();
    await expect(page.getByRole('button', { name: 'report.txt', exact: true })).toBeVisible();
  } finally { await removeProfile(page, csrf, profile); }
});

test('mounted folder picker remembers the selected folder and favorites', async ({ page }) => {
  const requests: Record<string, unknown>[] = [];
  await page.route('**/api/v1/destinations', async (route) => {
    await route.fulfill({ json: { destinations: [
      { id: 'server', label: 'Server downloads', available_bytes: null, available: true, browseable: false },
      { id: 'media', label: 'Media', available_bytes: null, available: true, browseable: true },
    ] } });
  });
  await page.route('**/api/v1/destinations/media/folders*', async (route) => {
    const path = new URL(route.request().url()).searchParams.get('path') ?? '';
    await route.fulfill({ json: { destination_id: 'media', path, folders: path === '' ? [{ name: 'Movies', path: 'Movies' }] : [] } });
  });
  await page.route('**/api/v1/preferences', async (route) => {
    if (route.request().method() === 'PUT') await route.fulfill({ json: route.request().postDataJSON() });
    else await route.continue();
  });
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() === 'POST') {
      requests.push(route.request().postDataJSON() as Record<string, unknown>);
      await route.fulfill({ status: 202, json: {} });
    } else await route.continue();
  });
  await page.getByRole('checkbox', { name: 'Select readme.txt', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  let dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText('To save into a host folder', { exact: false })).toHaveCount(0);
  await dialog.getByRole('combobox', { name: 'Destination', exact: true }).selectOption('media');
  await dialog.getByRole('button', { name: 'Movies/', exact: true }).click();
  await expect(dialog.getByText('Selected folder: /Movies', { exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Add favorite', exact: true }).click();
  await expect(dialog.getByRole('button', { name: 'Remove favorite', exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Queue files', exact: true }).click();
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({ destination_id: 'media', destination_path: 'Movies' });
  await expect(dialog).toBeHidden();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('combobox', { name: 'Destination', exact: true })).toHaveValue('media');
  await expect(dialog.getByText('Selected folder: /Movies', { exact: true })).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Media / Movies', exact: true })).toBeVisible();
});

test('find files scopes one folder and lets types change without another crawl', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
  const dialog = page.getByRole('dialog', { name: 'Find files', exact: true });
  await dialog.getByRole('radio', { name: 'This folder only' }).check();
  await dialog.getByRole('checkbox', { name: 'Image' }).check();
  await dialog.getByRole('checkbox', { name: 'Video' }).check();
  const created = page.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/profiles\/[^/]+\/scans$/.test(new URL(response.url()).pathname));
  await dialog.getByRole('button', { name: 'Find files', exact: true }).click();
  const request = await created;
  expect(request.request().postDataJSON()).toMatchObject({ max_depth: 0, categories: [] });
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole('article', { name: 'mountain.png', exact: true })).toBeVisible();
  await expect(page.getByRole('article', { name: 'unsupported-video.mp4', exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: 'Image' }).uncheck();
  await expect(page.getByRole('article', { name: 'mountain.png', exact: true })).toHaveCount(0);
  await expect(page.getByRole('article', { name: 'unsupported-video.mp4', exact: true })).toBeVisible();
  await navigation(page, 'Overview');
  await page.locator('.find-return').click();
  await expect(page.getByRole('article', { name: 'unsupported-video.mp4', exact: true })).toBeVisible();
  await expect(page.getByRole('article', { name: 'mountain.png', exact: true })).toHaveCount(0);
  await page.getByLabel('Filter results by name').fill('unsupported');
  await page.getByRole('button', { name: 'Show results', exact: true }).click();
  await navigation(page, 'Overview');
  await page.locator('.find-return').click();
  await expect(page.getByLabel('Filter results by name')).toHaveValue('unsupported');
});

test('an active file search remains reachable and can be cancelled', async ({ page }) => {
  await sourceState(page.request, { delay_ms: 750 });
  try {
    await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
    await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
    await navigation(page, 'Overview');
    await page.locator('.find-return').click();
    await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByText('Stopped finding files', { exact: true })).toBeVisible();
  } finally { await sourceState(page.request); }
});

test('browser downloads complete natively without creating server jobs', async ({ page }) => {
  const before = await page.request.get('/api/v1/downloads');
  const beforeJobs = (await before.json() as { downloads: unknown[] }).downloads.length;
  await page.getByRole('button', { name: 'readme.txt', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'readme.txt', exact: true });
  const received = page.waitForEvent('download');
  await dialog.getByRole('button', { name: 'Download', exact: true }).click();
  const download = await received;
  expect(download.suggestedFilename()).toBe('readme.txt');
  expect(await download.failure()).toBeNull();
  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  expect(Buffer.concat(chunks).toString()).toBe('Synthetic Folderhug browser fixture.\n');
  await expect(page.getByText('Download requested. Check your browser downloads.', { exact: true })).toBeVisible();
  const after = await page.request.get('/api/v1/downloads');
  expect((await after.json() as { downloads: unknown[] }).downloads.length).toBe(beforeJobs);
});

test('browser transfer works without server destination access', async ({ page }, testInfo) => {
  let destinationRequests = 0;
  await page.route('**/api/v1/destinations**', async (route) => {
    destinationRequests++;
    await route.fulfill({ status: 503, json: { code: 'unavailable', message: 'No destination access' } });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('checkbox', { name: 'Select readme.txt', exact: true }).check();
  await page.locator('.selection-bar').getByRole('button', { name: 'Download to this device' }).click();
  const dialog = page.getByRole('dialog', { name: 'Download to this device' });
  await expect(dialog).toContainText('Your browser saves these files on this device.');
  await expect(dialog.getByRole('combobox', { name: 'Destination' })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath('browser-transfer-desktop.png'), animations: 'disabled' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: testInfo.outputPath('browser-transfer-phone.png'), animations: 'disabled' });
  const received = page.waitForEvent('download');
  await dialog.getByRole('button', { name: 'Download', exact: true }).click();
  const download = await received;
  expect(download.suggestedFilename()).toBe('readme.txt');
  expect(await download.failure()).toBeNull();
  expect(destinationRequests).toBe(0);
});

test('server transfer explains its built-in location and optional host folders', async ({ page }, testInfo) => {
  await page.route('**/api/v1/destinations', async (route) => {
    await route.fulfill({ json: { destinations: [
      { id: 'server', label: 'Server downloads', available_bytes: null, available: true, browseable: false },
    ] } });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('checkbox', { name: 'Select readme.txt', exact: true }).check();
  await page.locator('.selection-bar').getByRole('button', { name: 'Save to server' }).click();
  const dialog = page.getByRole('dialog', { name: 'Save to server' });
  await expect(dialog.getByRole('combobox', { name: 'Destination' })).toHaveValue('server');
  await expect(dialog).toContainText("This is Folderhug's built-in downloads folder.");
  await expect(dialog).toContainText('To save into a host folder, mount it directly under /mnt in Compose and restart Folderhug.');
  await page.screenshot({ path: testInfo.outputPath('server-transfer-desktop.png'), animations: 'disabled' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: testInfo.outputPath('server-transfer-phone.png'), animations: 'disabled' });
});

test('server downloads complete and expose a separate browser download', async ({ page }, testInfo) => {
  const csrf = await csrfToken(page);
  const profile = await createProfile(page, csrf, `Completed transfer ${testInfo.project.name}`);
  try {
    await page.goto(`/#page=files&profile=${profile.id}&directory=root`);
    await page.reload();
    await page.getByRole('checkbox', { name: 'Select readme.txt', exact: true }).check();
    await page.getByRole('button', { name: 'Save to server', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Save to server', exact: true });
    await dialog.getByRole('button', { name: 'Queue files', exact: true }).click();
    await navigation(page, 'Server queue');
    const job = page.getByRole('article', { name: 'readme.txt', exact: true }).first();
    await expect(job.getByText('Completed', { exact: true })).toBeVisible({ timeout: 15_000 });
    const received = page.waitForEvent('download');
    await job.getByRole('button', { name: 'Download', exact: true }).click();
    const download = await received;
    expect(download.suggestedFilename()).toBe('readme.txt');
    expect(await download.failure()).toBeNull();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await job.getByRole('button', { name: 'Delete downloaded file', exact: true }).click();
    await page.getByRole('dialog', { name: 'Delete downloaded file?', exact: true }).getByRole('button', { name: 'Delete downloaded file', exact: true }).click();
    await expect(job).toContainText('no longer available');
  } finally { await removeProfile(page, csrf, profile); }
});

test('a streamed server transfer can pause, resume and cancel', async ({ page }, testInfo) => {
  const csrf = await csrfToken(page);
  const profile = await createProfile(page, csrf, `Paused transfer ${testInfo.project.name}`);
  let jobId = '';
  try {
    await page.goto(`/#page=files&profile=${profile.id}&directory=root`);
    await page.reload();
    await page.getByRole('checkbox', { name: 'Select slow-download.bin', exact: true }).check();
    await page.getByRole('button', { name: 'Save to server', exact: true }).click();
    const queued = page.waitForResponse((response) => response.url().endsWith('/api/v1/downloads') && response.request().method() === 'POST');
    await page.getByRole('dialog', { name: 'Save to server', exact: true }).getByRole('button', { name: 'Queue files', exact: true }).click();
    const response = await queued;
    expect(response.status()).toBe(202);
    jobId = (await response.json() as { id: string }).id;
    await navigation(page, 'Server queue');
    const job = page.getByRole('article', { name: 'slow-download.bin', exact: true }).first();
    await expect(job.getByText('Running', { exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('active-server-download.png'), fullPage: true });
    await job.getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(job.getByText('Paused', { exact: true })).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await job.getByRole('button', { name: 'Resume', exact: true }).click();
    await expect(job.getByRole('button', { name: 'Pause', exact: true })).toBeVisible();
    await job.getByRole('button', { name: 'Cancel transfer', exact: true }).click();
    await expect(job.getByText('Cancelled', { exact: true })).toBeVisible();
    const status = await page.request.get(`/api/v1/downloads/${jobId}`);
    expect((await status.json() as { state: string }).state).toBe('cancelled');
  } finally {
    if (jobId) await page.request.post(`/api/v1/downloads/${jobId}/cancel`, { headers: mutationHeaders(csrf) });
    await removeProfile(page, csrf, profile);
  }
});

test('images preview real bytes and unsupported video provides a fallback', async ({ page }, testInfo) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'mountain.png', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'mountain.png', exact: true });
  const image = dialog.getByRole('img', { name: 'mountain.png', exact: true });
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((element) => (element as HTMLImageElement).naturalWidth)).toBe(120);
  await dialog.getByRole('button', { name: 'Zoom in', exact: true }).click();
  await expect(dialog.getByRole('button', { name: 'Fit to window', exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Next image', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'sunset.jpg', exact: true }).getByRole('img')).toBeVisible();
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'unsupported-video.mp4', exact: true }).click();
  await expect(page.getByText('Preview unavailable. You can download the file to open it.', { exact: true })).toBeVisible({ timeout: 15_000 });
  await page.screenshot({ path: testInfo.outputPath('unavailable-video.png'), fullPage: true });
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});
