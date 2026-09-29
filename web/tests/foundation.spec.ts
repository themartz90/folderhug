import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { expireSessions, openLibrary, ownerPassword } from './fixture';

test('owner login uses the real session and rejects a wrong password', async ({ page }, testInfo) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Folderhug', exact: true })).toBeVisible();
  await expect(page.getByLabel('Owner password', { exact: true })).toBeVisible();
  if (testInfo.project.name === 'chromium') {
    for (const theme of ['dark', 'light'] as const) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('.app')).toHaveAttribute('data-theme', theme);
      await expect(page.getByRole('heading', { name: 'Folderhug', exact: true })).toBeVisible();
      await page.screenshot({ animations: 'disabled', path: testInfo.outputPath(`folderhug-login-${theme}.png`) });
    }
  }
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByLabel('Owner password', { exact: true }).fill('incorrect synthetic password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await page.getByLabel('Owner password', { exact: true }).fill(ownerPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
  const session = await page.request.get('/api/v1/auth/session');
  expect(session.status()).toBe(200);
  expect((await session.json() as { authenticated: boolean }).authenticated).toBe(true);
  await page.reload();
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
});

test('expired owner access returns to sign in without exposing cached files', async ({ page }) => {
  await openLibrary(page);
  await expireSessions(page.request);
  // A background request may already have returned to sign-in after expiry.
  await page.getByRole('button', { name: /Refresh folder|Refresh/, exact: true })
    .evaluateAll((buttons) => buttons.forEach((button) => (button as HTMLButtonElement).click()));
  await expect(page.getByLabel('Owner password', { exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Files', exact: true })).not.toBeVisible();
  expect((await page.request.get('/api/v1/profiles')).status()).toBe(401);
});
