import { expect, test } from '@playwright/test';
import { openLibrary } from './fixture';

const uuidV4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(crypto, 'randomUUID', { configurable: true, value: undefined });
  });
  await openLibrary(page);
  expect(await page.evaluate(() => typeof crypto.randomUUID)).toBe('undefined');
});

test('server transfer retries a lost response without crypto.randomUUID', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Save to server', exact: true })).toBeVisible();
  const submissions: { body: string | null; key: string | undefined; id: string }[] = [];
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    const response = await route.fetch();
    expect(response.ok()).toBeTruthy();
    const job = await response.json() as { id: string };
    submissions.push({ body: route.request().postData(), key: route.request().headers()['idempotency-key'], id: job.id });
    if (submissions.length === 1) await route.abort('failed');
    else await route.fulfill({ response });
  });
  await page.getByRole('button', { name: 'Queue files', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Retry uses the same request settings');
  await page.getByRole('button', { name: 'Queue files', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '1 file queued on the server.' })).toBeVisible();
  expect(submissions).toHaveLength(2);
  expect(submissions[0]?.key).toMatch(uuidV4);
  expect(submissions[1]).toEqual(submissions[0]);
  expect(errors).toEqual([]);
});

test('mounted destination keeps its folder in the submission without crypto.randomUUID', async ({ page }) => {
  await page.route('**/api/v1/destinations', async (route) => route.fulfill({ json: {
    destinations: [{ id: 'media', label: 'Synthetic mount', available: true, available_bytes: null, browseable: true }],
  } }));
  await page.route('**/api/v1/destinations/media/folders*', async (route) => {
    const path = new URL(route.request().url()).searchParams.get('path') ?? '';
    await route.fulfill({ json: { destination_id: 'media', path, folders: path ? [] : [{ name: 'Movies', path: 'Movies' }] } });
  });
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Save to server', exact: true });
  await dialog.getByRole('combobox', { name: /^Destination/ }).selectOption('media');
  await dialog.getByRole('button', { name: 'Movies/', exact: true }).click();
  await expect(dialog.getByText('Selected folder: /Movies', { exact: true })).toBeVisible();
  let submitted: { body: Record<string, unknown>; key: string | undefined } | undefined;
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    submitted = { body: route.request().postDataJSON(), key: route.request().headers()['idempotency-key'] };
    await route.fulfill({ status: 201, json: {} });
  });
  await dialog.getByRole('button', { name: 'Queue files', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '1 file queued on the server.' })).toBeVisible();
  expect(submitted?.body).toMatchObject({ destination_id: 'media', destination_path: 'Movies' });
  expect(submitted?.key).toMatch(uuidV4);
});

test('scan retries a lost response without crypto.randomUUID', async ({ page }) => {
  await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
  const dialog = page.getByRole('dialog', { name: 'Find files', exact: true });
  await expect(dialog).toBeVisible();
  const submissions: { body: string | null; key: string | undefined; id: string }[] = [];
  await page.route('**/api/v1/profiles/*/scans', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    const response = await route.fetch();
    expect(response.ok()).toBeTruthy();
    const job = await response.json() as { id: string };
    submissions.push({ body: route.request().postData(), key: route.request().headers()['idempotency-key'], id: job.id });
    if (submissions.length === 1) await route.abort('failed');
    else await route.fulfill({ response });
  });
  await dialog.getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Retry uses the same request settings');
  await dialog.getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Search results', exact: true })).toBeVisible();
  expect(submissions).toHaveLength(2);
  expect(submissions[0]?.key).toMatch(uuidV4);
  expect(submissions[1]).toEqual(submissions[0]);
});

test('filter preset persists without crypto.randomUUID', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'More filters', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Folder filters', exact: true });
  await dialog.getByLabel('Extensions, separated by commas', { exact: true }).fill('png');
  await dialog.getByLabel('Preset name', { exact: true }).fill('HTTP PNG files');
  await dialog.getByRole('button', { name: 'Save current filters', exact: true }).click();
  await expect(dialog.getByRole('button', { name: 'HTTP PNG files', exact: true })).toBeVisible();
  const preferences = await (await page.request.get('/api/v1/preferences')).json() as { filter_presets: { id: string; name: string }[] };
  expect(preferences.filter_presets.find((preset) => preset.name === 'HTTP PNG files')?.id).toMatch(uuidV4);
  await page.reload();
  await page.getByRole('button', { name: 'More filters', exact: true }).click();
  await dialog.getByRole('button', { name: 'HTTP PNG files', exact: true }).click();
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'sunset.jpg', exact: true })).not.toBeVisible();
});
