import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { openLibrary, resetPreferences, signIn } from './fixture';

const release = {
  current: { version: 'v1.2.3', revision: 'synthetic', channel: 'stable' },
  highlights: { version: 'v1.2.3', auto_show: false, summary: 'A few changes for browsing.', highlights: ['Version details beside Settings.', 'A summary of installed changes.'] },
};
const current = { state: 'current', latest: { version: 'v1.2.3', release_url: 'https://github.com/themartz90/folderhug/releases/tag/v1.2.3', published_at: '2026-10-16T12:00:00Z' }, checked_at: new Date().toISOString(), stale: false, retry_at: null };

async function mockRelease(page: Page, data = release, status: Record<string, unknown> = current) {
  await page.route('**/api/v1/release', (route) => route.fulfill({ json: data }));
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: status }));
}

test('version beside Settings opens installed notes and a newer release has a separate link', async ({ page }, testInfo) => {
  await mockRelease(page, release, { ...current, state: 'available', latest: { ...current.latest, version: 'v1.3.0', release_url: 'https://github.com/themartz90/folderhug/releases/tag/v1.3.0' } });
  await openLibrary(page);
  const button = page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true });
  await expect(button).toBeVisible();
  const link = page.getByRole('link', { name: /Update available: v1.3.0/ });
  await expect(link).toHaveAttribute('href', 'https://github.com/themartz90/folderhug/releases/tag/v1.3.0');
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  await button.click();
  const dialog = page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true });
  await expect(dialog).toContainText(release.highlights.summary);
  await expect(dialog.getByRole('link', { name: /Full release notes/ })).toHaveAttribute('href', current.latest.release_url);
  if (testInfo.project.name === 'chromium') expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.keyboard.press('Escape');
  await expect(button).toBeFocused();
});

test('unseen highlights open once, reopen manually, and do not repeat after reload', async ({ page }) => {
  await mockRelease(page, { ...release, highlights: { ...release.highlights, auto_show: true } });
  const csrf = await signIn(page);
  await resetPreferences(page, csrf);
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await page.reload();
  await expect(page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true })).toBeVisible();
  await expect(dialog).not.toBeVisible();
  await page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true }).click();
  await expect(dialog).toBeVisible();
});

test('notes arriving in an already open release dialog count as seen', async ({ page }) => {
  let deliver!: () => void;
  const ready = new Promise<void>((resolve) => { deliver = resolve; });
  await page.route('**/api/v1/release', async (route) => {
    await ready;
    await route.fulfill({ json: { ...release, highlights: { ...release.highlights, auto_show: true } } });
  });
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: current }));
  await openLibrary(page);
  await page.getByRole('button', { name: 'Folderhug version details', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Folderhug version details', exact: true })).toBeVisible();
  deliver();
  const dialog = page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true });
  await expect(dialog).toContainText(release.highlights.summary);
  await expect.poll(() => page.evaluate(() => localStorage.getItem('folderhug.release-highlights.seen.v1.2.3'))).toBe('seen');
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true })).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('automatic notes wait until an existing dialog closes', async ({ page }) => {
  let deliver!: () => void;
  const ready = new Promise<void>((resolve) => { deliver = resolve; });
  await page.route('**/api/v1/release', async (route) => {
    await ready;
    await route.fulfill({ json: { ...release, highlights: { ...release.highlights, auto_show: true } } });
  });
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: current }));
  await openLibrary(page);
  await page.getByRole('button', { name: 'Appearance', exact: true }).click();
  deliver();
  const appearance = page.getByRole('dialog', { name: 'Appearance', exact: true });
  await expect(page.locator('.sidebar .release-version')).toContainText('v1.2.3');
  await expect(appearance).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true })).toBeVisible();
});

test('signing out discards a late release response', async ({ page }) => {
  let deliver!: () => void;
  const ready = new Promise<void>((resolve) => { deliver = resolve; });
  await page.route('**/api/v1/release', async (route) => {
    await ready;
    await route.fulfill({ json: release }).catch(() => {});
  });
  await openLibrary(page);
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  deliver();
  await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true })).toHaveCount(0);
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('automatic notes wait for a dialog opened inside Settings', async ({ page }) => {
  let deliver!: () => void;
  const ready = new Promise<void>((resolve) => { deliver = resolve; });
  await page.route('**/api/v1/release', async (route) => {
    await ready;
    await route.fulfill({ json: { ...release, highlights: { ...release.highlights, auto_show: true } } });
  });
  await page.route('**/api/v1/updates', (route) => route.fulfill({ json: current }));
  await openLibrary(page);
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('button', { name: 'Create token', exact: true }).click();
  deliver();
  await expect(page.locator('.sidebar .release-version')).toContainText('v1.2.3');
  await expect(page.getByRole('dialog', { name: 'Create API token', exact: true })).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true })).toBeVisible();
});

test('current, stale, ahead, disabled and development checks use honest status copy', async ({ page }) => {
  for (const [state, stale, text] of [
    ['current', false, "You're on the latest release."],
    ['current', true, 'Could not check for updates.'],
    ['ahead', false, 'This build is newer than the latest stable release.'],
    ['disabled', false, 'Update checks are disabled on this server.'],
    ['development', false, 'Development build. Update checks are skipped.'],
  ] as const) {
    await mockRelease(page, release, { ...current, state, stale });
    await openLibrary(page);
    await page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true }).click();
    await expect(page.getByRole('dialog')).toContainText(text);
    await expect(page.locator('.sidebar .release-update')).toHaveCount(0);
    await page.keyboard.press('Escape');
  }
});

test('an equal release check expires while the browser stays offline', async ({ page, context }) => {
  await page.clock.install({ time: new Date('2030-01-01T00:00:00Z') });
  await mockRelease(page, release, { ...current, checked_at: '2030-01-01T00:00:00Z' });
  await openLibrary(page);
  const version = page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true });
  await version.click();
  await expect(page.getByRole('dialog')).toContainText("You're on the latest release.");
  await page.keyboard.press('Escape');
  await context.setOffline(true);
  await page.clock.fastForward(12 * 60 * 60 * 1000 + 1000);
  await version.click();
  await expect(page.getByRole('dialog')).toContainText('Could not check for updates.');
  await expect(page.getByRole('dialog')).toContainText('Last successful check');
  await expect(page.getByRole('dialog')).not.toContainText("You're on the latest release.");
});

test('release controls and notes fit desktop, collapsed sidebar and phone in both themes', async ({ page }, testInfo) => {
  await mockRelease(page, release, { ...current, state: 'available', latest: { ...current.latest, version: 'v1.3.0' } });
  await openLibrary(page);
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme });
    for (const [width, height] of [[1440, 900], [1024, 768], [390, 844]] as const) {
      await page.setViewportSize({ width, height });
      if (width === 390) await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
      const version = page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true });
      await expect(version).toBeVisible();
      const box = (await version.boundingBox())!;
      expect(box.y + box.height).toBeLessThanOrEqual(height);
      if (width === 390) expect(box.height).toBeGreaterThanOrEqual(44);
      if (testInfo.project.name === 'chromium') await page.screenshot({ path: testInfo.outputPath(`release-sidebar-${theme}-${width}.png`) });
      await version.click();
      const dialog = page.getByRole('dialog');
      await expect(dialog).toBeVisible();
      const dialogBox = (await dialog.boundingBox())!;
      expect(dialogBox.x).toBeGreaterThanOrEqual(0);
      expect(dialogBox.x + dialogBox.width).toBeLessThanOrEqual(width);
      expect(dialogBox.y + dialogBox.height).toBeLessThanOrEqual(height);
      if (testInfo.project.name === 'chromium') await page.screenshot({ path: testInfo.outputPath(`release-dialog-${theme}-${width}.png`) });
      await page.keyboard.press('Escape');
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
    await expect(page.locator('.app-shell')).toHaveClass(/collapsed/);
    await expect(page.locator('.sidebar .release-version')).toBeVisible();
    await expect(page.locator('.sidebar .release-update')).toBeVisible();
    if (testInfo.project.name === 'chromium') await page.screenshot({ path: testInfo.outputPath(`release-collapsed-${theme}.png`) });
    await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
  }
});

test('blocked storage suppresses automatic notes but keeps manual opening and literal text', async ({ page }) => {
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => { throw new Error('blocked'); };
    Storage.prototype.setItem = () => { throw new Error('blocked'); };
  });
  await mockRelease(page, { ...release, highlights: { ...release.highlights, auto_show: true, summary: '<img src=x onerror=alert(1)>' } });
  await openLibrary(page);
  const button = page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true });
  await expect(button).toBeVisible();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await button.click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('<img src=x onerror=alert(1)>');
  await expect(dialog.locator('img')).toHaveCount(0);
});

test('offline update lookup and missing notes leave the installed version usable', async ({ page }) => {
  await mockRelease(page, { ...release, highlights: null } as unknown as typeof release);
  await page.route('**/api/v1/updates', (route) => route.abort());
  await openLibrary(page);
  await page.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('No summary is included for this build.');
  await expect(page.getByRole('dialog')).toContainText('Could not check for updates.');
  await expect(page.locator('.error-banner')).toHaveCount(0);
});

test('mobile navigation closes before notes and focus returns to the navigation toggle', async ({ page }) => {
  await mockRelease(page);
  await openLibrary(page);
  await page.setViewportSize({ width: 390, height: 844 });
  const toggle = page.getByRole('button', { name: 'Toggle sidebar', exact: true });
  await toggle.click();
  const navigation = page.getByRole('dialog', { name: 'Profiles and navigation', exact: true });
  await navigation.getByRole('button', { name: "What's new in Folderhug v1.2.3", exact: true }).click();
  await expect(navigation).not.toBeVisible();
  await expect(page.getByRole('dialog', { name: "What's new in Folderhug v1.2.3", exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(toggle).toBeFocused();
});
