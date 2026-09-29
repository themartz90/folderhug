import { expect, test } from '@playwright/test';

const setupOrigin = 'http://127.0.0.1:4175';
const password = '🔐 owner pass';

test('first browser visit claims the owner, remembers a session, and can sign out every device', async ({ page, browser, browserName }, testInfo) => {
  test.skip(browserName !== 'chromium', 'The isolated first-run fixture is claimed once.');
  await page.goto(setupOrigin);
  await expect(page.getByRole('button', { name: 'Create owner account' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('folderhug-owner-setup.png'), animations: 'disabled' });
  await expect(page.getByLabel('Remember me')).not.toBeChecked();
  await page.getByLabel('Owner password').fill('🔐abc');
  await page.getByLabel('Repeat password').fill('🔐abc');
  await page.getByRole('button', { name: 'Create owner account' }).click();
  await expect(page.getByRole('alert')).toContainText('at least 6 characters');
  await page.getByLabel('Owner password').fill(password);
  await page.getByLabel('Repeat password').fill('wrong password');
  await page.getByRole('button', { name: 'Create owner account' }).click();
  await expect(page.getByRole('alert')).toContainText('do not match');
  await page.getByLabel('Owner password').fill(password);
  await page.getByLabel('Repeat password').fill(password);
  await page.getByLabel('Remember me').check();
  const claim = page.waitForResponse(`${setupOrigin}/api/v1/auth/setup`);
  await page.getByRole('button', { name: 'Create owner account' }).click();
  const response = await claim;
  expect(response.status()).toBe(200);
  expect(response.request().postDataJSON()).toEqual({ password, remember_me: true });
  await expect(page.getByRole('heading', { name: 'Add your first profile' })).toBeVisible();
  const cookie = (await page.context().cookies(setupOrigin)).find((item) => item.name === 'folderhug_session');
  expect(cookie?.expires).toBeGreaterThan(Date.now() / 1000 + 60 * 60 * 24 * 80);

  const other = await browser.newContext();
  try {
    const otherPage = await other.newPage();
    await otherPage.goto(setupOrigin);
    await expect(otherPage.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
    await otherPage.getByLabel('Owner password').fill(password);
    await otherPage.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(otherPage.getByRole('heading', { name: 'Add your first profile' })).toBeVisible();
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await page.getByRole('button', { name: 'Sign out all devices' }).click();
    await expect(page.getByRole('dialog', { name: 'Sign out all devices' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Sign out all devices' }).getByRole('button', { name: 'Sign out all devices' }).click();
    await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
    await expect(page.getByLabel('Remember me')).not.toBeChecked();
    expect((await otherPage.request.get(`${setupOrigin}/api/v1/auth/session`)).status()).toBe(401);
    await page.getByLabel('Owner password').fill(password);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
  } finally {
    await other.close();
  }
});
