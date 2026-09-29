import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { createProfile, navigation, openLibrary, removeProfile } from './fixture';

test('overview gives each profile browse, edit, and confirmed remove actions', async ({ page }, testInfo) => {
  const csrf = await openLibrary(page);
  const profile = await createProfile(page, csrf, `Overview actions ${testInfo.project.name}`);
  let removed = false;
  try {
    await page.reload();
    await navigation(page, 'Overview');
    const card = page.locator('.profile-card').filter({ hasText: profile.name });
    await expect(card).toBeVisible();
    if (testInfo.project.name === 'chromium') {
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    }
    await card.getByRole('button', { name: 'Edit', exact: true }).click();
    await expect(page.getByRole('dialog', { name: 'Edit profile' })).toBeVisible();
    await page.keyboard.press('Escape');
    await card.getByRole('button', { name: 'Remove', exact: true }).click();
    const confirmation = page.getByRole('dialog', { name: 'Remove profile' });
    await expect(confirmation).toContainText(profile.name);
    await confirmation.getByRole('button', { name: 'Keep profile' }).click();
    await expect(card).toBeVisible();
    await card.getByRole('button', { name: 'Remove', exact: true }).click();
    await confirmation.getByRole('button', { name: 'Remove profile', exact: true }).click();
    await expect(card).toHaveCount(0);
    removed = true;
  } finally {
    if (!removed) await removeProfile(page, csrf, profile);
  }
});

test('explorer controls stay together from wide desktop to phone', async ({ page }, testInfo) => {
  await openLibrary(page);
  await page.getByRole('button', { name: 'Grid view' }).click();
  for (const width of [2560, 1024, 390]) {
    await page.setViewportSize({ width, height: width === 2560 ? 1440 : 844 });
    const find = page.getByRole('button', { name: 'Find files' });
    const edit = page.getByRole('button', { name: 'Edit profile' });
    await expect(find).toBeVisible();
    await expect(edit).toBeVisible();
    const findBox = await find.boundingBox();
    const headingBox = await page.locator('.folder-heading').boundingBox();
    expect(findBox).not.toBeNull();
    expect(headingBox).not.toBeNull();
    if (width === 2560) expect(findBox!.x - headingBox!.x).toBeLessThan(600);
    expect(findBox!.x + findBox!.width).toBeLessThanOrEqual(width);
    const type = page.getByLabel('File type');
    await expect(type).toBeVisible();
    const typeBox = await type.boundingBox();
    expect(typeBox!.x + typeBox!.width).toBeLessThanOrEqual(width);
    for (const control of [page.getByLabel('Grid size'), page.getByLabel('Folders first'), page.getByLabel('Sort by'), page.getByRole('button', { name: 'Grid view', exact: true })]) {
      await expect(control).toBeVisible();
      const box = (await control.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
    }
    await expect(page.locator('.virtual-grid .file-name').first()).toHaveCSS('font-size', '13px');
    if (testInfo.project.name === 'chromium') {
      await page.screenshot({ path: testInfo.outputPath(`explorer-${width}.png`) });
    }
  }
  await navigation(page, 'Overview');
  if (testInfo.project.name === 'chromium') {
    await page.screenshot({ path: testInfo.outputPath('overview-phone.png') });
    await page.setViewportSize({ width: 2560, height: 1440 });
    await page.emulateMedia({ colorScheme: 'dark' });
    await expect(page.locator('.app')).toHaveAttribute('data-theme', 'dark');
    await expect(page.locator('.sidebar .primary-nav button.active')).toHaveCSS('background-color', 'rgb(33, 34, 38)');
    await page.screenshot({ path: testInfo.outputPath('overview-dark-2560.png') });
    await navigation(page, 'Synthetic library');
    await page.getByRole('button', { name: 'Grid view' }).click();
    await page.screenshot({ path: testInfo.outputPath('explorer-dark-2560.png') });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  }
});

test('branded grid keeps long names, extensions and keyboard focus readable', async ({ page }, testInfo) => {
  await openLibrary(page);
  // A reload must keep an existing owner's accent choice.
  await page.reload();
  await expect(page.locator('.app')).toHaveCSS('--accent', '#285ED1');
  await page.getByRole('button', { name: 'Appearance', exact: true }).click();
  await page.getByRole('radio', { name: 'Amber', exact: true }).check();
  await expect(page.locator('.app')).toHaveCSS('--accent', '#95630A');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Appearance', exact: true })).not.toBeVisible();
  await page.reload();
  await page.getByRole('button', { name: 'Large', exact: true }).click();
  await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('long filename');
  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  const tile = page.getByRole('button', { name: /^long filename/ });
  await expect(tile).toBeVisible();
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme });
    await expect(page.locator('.app')).toHaveAttribute('data-theme', theme);
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 });
      for (const size of ['small', 'medium', 'large']) {
        await page.getByLabel('Grid size').selectOption(size);
        const name = tile.locator('.file-name');
        await expect(name).toHaveCSS('font-size', '13px');
        const colors = await tile.evaluate((element) => ({
          name: getComputedStyle(element.querySelector('.file-name')!).color,
          tile: getComputedStyle(element).color,
        }));
        expect(colors.name).toBe(colors.tile);
        const extension = name.locator('span').last();
        await expect(extension).toHaveText('.txt');
        const nameBox = (await name.boundingBox())!;
        const extensionBox = (await extension.boundingBox())!;
        const tileBox = (await tile.boundingBox())!;
        const rowBox = (await tile.locator('..').boundingBox())!;
        const detailsBox = (await tile.locator('..').getByRole('button', { name: /^Details for/ }).boundingBox())!;
        expect(extensionBox.x + extensionBox.width).toBeLessThanOrEqual(tileBox.x + tileBox.width);
        expect(extensionBox.y + extensionBox.height).toBeLessThanOrEqual(nameBox.y + nameBox.height + 1);
        expect(nameBox.height).toBeLessThanOrEqual(size === 'small' ? 20 : 38);
        expect(nameBox.y + nameBox.height).toBeLessThanOrEqual(detailsBox.y);
        expect(tileBox.y + tileBox.height).toBeLessThanOrEqual(rowBox.y + rowBox.height);
        await tile.locator('..').getByRole('checkbox').focus();
        await page.keyboard.press('Tab');
        await expect(tile).toBeFocused();
        await expect(tile).toHaveCSS('outline-style', 'solid');
        if (testInfo.project.name === 'chromium' && size === 'medium') {
          await page.screenshot({ animations: 'disabled', path: testInfo.outputPath(`folderhug-${theme}-${width}.png`) });
        }
      }
    }
    if (testInfo.project.name === 'chromium') expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await navigation(page, 'Synthetic library');
  await page.getByLabel('Grid size').selectOption('medium');
  await expect(page.locator('.sidebar .current-profile')).toHaveAttribute('aria-current', 'page');
  await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
  await expect(page.locator('.sidebar .folderhug-mark')).toBeVisible();
  await expect(page.locator('.sidebar .folderhug-label')).not.toBeVisible();
  await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
  if (testInfo.project.name === 'chromium') {
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('.app')).toHaveAttribute('data-theme', theme);
      await page.screenshot({ animations: 'disabled', path: testInfo.outputPath(`folderhug-library-${theme}.png`) });
    }
  }
});
