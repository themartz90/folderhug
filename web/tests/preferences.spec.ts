import { expect, request, test, type Page } from '@playwright/test';
import type { Preferences, Schema } from '../src/lib/api';
import {
  createProfile,
  fixtureInfo,
  fixtureOrigin,
  mutationHeaders,
  navigation,
  removeProfile,
  signIn,
} from './fixture';

async function openProfile(page: Page, profileId: string) {
  await page.goto(`/#page=files&profile=${encodeURIComponent(profileId)}&directory=root`);
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
}

test('saved filters can be renamed, reapplied after reload, and removed', async ({ page }, testInfo) => {
  const csrf = await signIn(page);
  const profile = await createProfile(page, csrf, `Preset profile ${testInfo.project.name}`);
  const original = `Images ${testInfo.project.name}`;
  const renamed = `PNG images ${testInfo.project.name}`;
  try {
    await openProfile(page, profile.id);
    await page.getByRole('button', { name: 'Media', exact: true }).click();
    await page.getByRole('button', { name: 'More filters', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Folder filters', exact: true });
    await dialog.getByLabel('Extensions, separated by commas', { exact: true }).fill('png');
    await dialog.getByRole('checkbox', { name: 'Show folders', exact: true }).uncheck();
    await dialog.getByLabel('Preset name', { exact: true }).fill(original);
    await dialog.getByRole('button', { name: 'Save current filters', exact: true }).click();
    await expect(dialog.getByRole('button', { name: original, exact: true })).toBeVisible();
    await dialog.getByRole('button', { name: `Rename preset ${original}`, exact: true }).click();
    await dialog.getByLabel('Preset name', { exact: true }).fill(renamed);
    await dialog.getByRole('button', { name: 'Rename preset', exact: true }).click();
    await expect(dialog.getByRole('button', { name: renamed, exact: true })).toBeVisible();
    await expect(dialog.getByRole('button', { name: original, exact: true })).toHaveCount(0);
    await page.keyboard.press('Escape');
    await page.reload();
    await expect(page.getByRole('button', { name: 'sunset.jpg', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'More filters', exact: true }).click();
    await dialog.getByRole('button', { name: renamed, exact: true }).click();
    await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'sunset.jpg', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Remove extensions filter', exact: true })).toContainText('png');
    await page.getByRole('button', { name: 'More filters', exact: true }).click();
    await dialog.getByRole('button', { name: `Remove preset ${renamed}`, exact: true }).click();
    await expect(dialog.getByRole('button', { name: renamed, exact: true })).toHaveCount(0);
    await page.keyboard.press('Escape');
    await page.reload();
    await page.getByRole('button', { name: 'More filters', exact: true }).click();
    await expect(dialog.getByRole('button', { name: renamed, exact: true })).toHaveCount(0);
  } finally {
    const response = await page.request.get('/api/v1/preferences');
    if (response.ok()) {
      const preferences = await response.json() as Preferences;
      await page.request.put('/api/v1/preferences', {
        headers: mutationHeaders(csrf),
        data: { ...preferences, filter_presets: preferences.filter_presets.filter((preset) => preset.profile_id !== profile.id) },
      });
    }
    await removeProfile(page, csrf, profile);
  }
});

test('a pinned folder survives reload and can be opened and unpinned', async ({ page }, testInfo) => {
  const csrf = await signIn(page);
  const profile = await createProfile(page, csrf, `Bookmark profile ${testInfo.project.name}`);
  try {
    await openProfile(page, profile.id);
    await page.getByRole('button', { name: 'Media', exact: true }).click();
    await page.getByRole('button', { name: 'Pin folder', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Unpin folder', exact: true })).toBeVisible();
    const bookmark = page.locator('.pinned-nav').getByTitle(`${profile.name} / Media`, { exact: true });
    await expect(bookmark).toBeVisible();
    await page.reload();
    await expect(bookmark).toBeVisible();
    await navigation(page, profile.name);
    await expect(page.getByRole('button', { name: 'readme.txt', exact: true })).toBeVisible();
    await bookmark.click();
    await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Unpin folder', exact: true }).click();
    await expect(bookmark).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Pin folder', exact: true })).toBeVisible();
    await page.reload();
    await expect(bookmark).toHaveCount(0);
    const response = await page.request.get('/api/v1/bookmarks');
    const data = await response.json() as { bookmarks: Schema['Bookmark'][] };
    expect(data.bookmarks.filter((item) => item.profile_id === profile.id)).toEqual([]);
  } finally { await removeProfile(page, csrf, profile); }
});

test('a scoped API token reveals its secret once and revocation removes access', async ({ page }, testInfo) => {
  const csrf = await signIn(page);
  const fixture = await fixtureInfo(page.request);
  const name = `Read-only token ${testInfo.project.name}`;
  let client: Awaited<ReturnType<typeof request.newContext>> | undefined;
  try {
    await page.goto('/#page=settings');
    await page.getByRole('button', { name: 'Create token', exact: true }).click();
    const create = page.getByRole('dialog', { name: 'Create API token', exact: true });
    await create.getByLabel('Name', { exact: true }).fill(name);
    await create.getByRole('checkbox', { name: 'Synthetic library', exact: true }).check();
    await expect(create.getByRole('checkbox', { name: /^Browse and search/ })).toBeChecked();
    await expect(create.getByRole('checkbox', { name: /^Read file content/ })).not.toBeChecked();
    await create.getByLabel('Expires at (local time, optional)', { exact: true }).fill(new Date(Date.now() + 86_400_000).toISOString().slice(0, 16));
    const created = page.waitForResponse((response) => response.url().endsWith('/api/v1/tokens') && response.request().method() === 'POST');
    await create.getByRole('button', { name: 'Create token', exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const result = await response.json() as { token: Schema['Token']; secret: string };
    expect(result.token.profile_ids).toEqual([fixture.profiles.library.id]);
    expect(result.token.scopes).toEqual(['browse:read']);
    expect(result.token.expires_at).not.toBeNull();
    const secretDialog = page.getByRole('dialog', { name: 'Copy your API token', exact: true });
    const secret = await secretDialog.getByLabel('API token', { exact: true }).inputValue();
    expect(secret).toBe(result.secret);
    expect(secret.length).toBeGreaterThan(20);
    client = await request.newContext({ baseURL: fixtureOrigin, extraHTTPHeaders: { Authorization: `Bearer ${secret}` } });
    const permitted = await client.get(`/api/v1/profiles/${fixture.profiles.library.id}/browse`);
    expect(permitted.status()).toBe(200);
    const listing = await permitted.json() as Schema['BrowsePage'];
    const file = listing.entries.find((entry) => entry.name === 'readme.txt');
    expect(file).toBeDefined();
    expect((await client.get(`/api/v1/profiles/${fixture.profiles.library.id}/entries/${file!.id}/content`)).status()).toBe(403);
    expect([403, 404]).toContain((await client.get(`/api/v1/profiles/${fixture.profiles.locked.id}/browse`)).status());
    await secretDialog.getByRole('button', { name: 'Done', exact: true }).click();
    await expect(page.getByLabel('API token', { exact: true })).toHaveCount(0);
    await navigation(page, 'Overview');
    await navigation(page, 'Settings');
    await page.reload();
    const token = page.getByRole('article', { name, exact: true });
    await expect(token).toContainText('browse:read');
    await expect(token).toContainText('Synthetic library');
    expect(await page.locator('body').textContent()).not.toContain(secret);
    const metadata = await page.request.get('/api/v1/tokens');
    expect(await metadata.text()).not.toContain(secret);
    await token.getByRole('button', { name: 'Revoke', exact: true }).click();
    await page.getByRole('dialog', { name: 'Revoke API token?', exact: true }).getByRole('button', { name: 'Revoke token', exact: true }).click();
    await expect(token).toHaveCount(0);
    expect((await client.get(`/api/v1/profiles/${fixture.profiles.library.id}/browse`)).status()).toBe(401);
  } finally {
    await client?.dispose();
    const response = await page.request.get('/api/v1/tokens');
    if (response.ok()) {
      const data = await response.json() as { tokens: Schema['Token'][] | null };
      for (const token of data.tokens ?? []) {
        if (token.name === name) await page.request.delete(`/api/v1/tokens/${token.id}`, { headers: mutationHeaders(csrf) });
      }
    }
  }
});

test('find results stay scoped and select only files for the queue', async ({ page }, testInfo) => {
  const csrf = await signIn(page);
  const profile = await createProfile(page, csrf, `Review profile ${testInfo.project.name}`);
  try {
    await openProfile(page, profile.id);
    await page.getByRole('button', { name: 'Scan', exact: true }).click();
    const directory = new URLSearchParams(new URL(page.url()).hash.slice(1)).get('directory');
    expect(directory).toBeTruthy();
    const searched = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/search');
    await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
    await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
    const response = await searched;
    const scope = new URL(response.url()).searchParams;
    expect(scope.get('profile_id')).toBe(profile.id);
    expect(scope.get('directory_id')).toBe(directory);
    expect(scope.get('q')).toBeNull();
    await expect(page.getByLabel('Filter results by name', { exact: true })).toHaveValue('');
    await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('article', { name: 'notes.txt', exact: true })).toBeVisible();
    await expect(page.getByRole('article', { name: 'report.txt', exact: true })).toBeVisible();
    await expect(page.getByRole('article', { name: 'Nested', exact: true })).toHaveCount(0);
    await page.getByRole('checkbox', { name: 'Select files on this page', exact: true }).check();
    await expect(page.getByText('2 selected', { exact: true })).toBeVisible();
    await page.locator('.activity-actions').getByRole('button', { name: 'Save to server', exact: true }).click();
    const queue = page.getByRole('dialog', { name: 'Save to server', exact: true });
    await expect(queue.locator('.transfer-files li')).toHaveCount(2);
    await expect(queue).toContainText('notes.txt');
    await expect(queue).toContainText('report.txt');
    await expect(queue.getByRole('button', { name: 'Queue files', exact: true })).toBeEnabled();
    await queue.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Search results', exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
    const scans = await page.request.get(`/api/v1/scans?profile_id=${profile.id}`);
    expect((await scans.json() as { scans: Schema['Scan'][] }).scans).toHaveLength(1);
  } finally { await removeProfile(page, csrf, profile); }
});
