import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { writeFile } from 'node:fs/promises';
import { fixtureInfo, mutationHeaders, navigation, openLibrary, sourceCredentials, sourceState } from './fixture';

test.beforeEach(async ({ page }) => { await openLibrary(page); });

test('folder navigation restores browser history and shows empty folders', async ({ page }, testInfo) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
  const mediaURL = page.url();
  await page.goBack();
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
  await page.goForward();
  await expect(page).toHaveURL(mediaURL);
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
  await page.goBack();
  await page.getByRole('button', { name: 'Empty', exact: true }).click();
  await expect(page.getByText('This folder is empty.', { exact: true })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('empty-folder.png'), fullPage: true });
});

test('selection survives views and filters and clears on a different folder', async ({ page }, testInfo) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  for (const view of ['Grid view', 'Compact view', 'Detailed view']) {
    await page.getByRole('button', { name: view, exact: true }).click();
    await expect(page.getByRole('checkbox', { name: 'Select mountain.png', exact: true })).toBeChecked();
    if (view === 'Grid view') {
      await expect(page.getByRole('button', { name: 'mountain.png', exact: true }).locator('img')).toBeVisible();
      const size = page.getByRole('combobox', { name: 'Grid size', exact: true });
      const tile = page.getByRole('button', { name: 'mountain.png', exact: true });
      await size.selectOption('small');
      const smallWidth = (await tile.boundingBox())!.width;
      await expect(page.getByRole('checkbox', { name: 'Select mountain.png', exact: true })).toBeChecked();
      await size.selectOption('large');
      await expect.poll(async () => (await tile.boundingBox())!.width).toBeGreaterThan(smallWidth);
      await expect(page.getByRole('checkbox', { name: 'Select mountain.png', exact: true })).toBeChecked();
      expect(await page.evaluate(() => localStorage.getItem('folderhug.grid-size'))).toBe('large');
      await page.screenshot({ path: testInfo.outputPath('grid-thumbnails.png'), fullPage: true });
    }
  }
  await page.getByRole('combobox', { name: 'File type', exact: true }).selectOption('video');
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).not.toBeVisible();
  await expect(page.getByText(/1 hidden/)).toBeVisible();
  await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('no-synthetic-match');
  await expect(page.getByText('No files match these filters.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Clear filters', exact: true }).first().click();
  await expect(page.getByRole('checkbox', { name: 'Select mountain.png', exact: true })).toBeChecked();
  await page.getByRole('combobox', { name: 'Sort by', exact: true }).selectOption('size');
  await expect(page.getByRole('columnheader', { name: /Size/ })).toHaveAttribute('aria-sort', 'ascending');
  await page.getByRole('button', { name: 'Ascending', exact: true }).click();
  await expect(page.getByRole('columnheader', { name: /Size/ })).toHaveAttribute('aria-sort', 'descending');
  await navigation(page, 'Synthetic library');
  await expect(page.getByRole('checkbox', { name: 'Select readme.txt', exact: true })).not.toBeChecked();
});

test('folder-scoped search survives reload and browser history', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  const directory = new URLSearchParams(new URL(page.url()).hash.slice(1)).get('directory');
  expect(directory).toBeTruthy();
  expect(directory).not.toBe('root');
  await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByText('Finished finding files', { exact: true })).toBeVisible({ timeout: 15_000 });
  for (const navigate of [
    async () => { await navigation(page, 'Overview'); await page.locator('.find-return').click(); },
    () => page.reload(),
    async () => { await navigation(page, 'Overview'); await page.goBack(); },
  ]) {
    const searched = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/search');
    await navigate();
    const response = await searched;
    expect(response.status()).toBe(200);
    expect(new URL(response.url()).searchParams.get('directory_id')).toBe(directory);
    await expect(page.getByText(/This folder and subfolders/)).toBeVisible();
  }
});

test('locked, unavailable and unsupported sources have usable failure states', async ({ page }, testInfo) => {
  await navigation(page, 'Locked source');
  await expect(page.getByText(/credentials|password needed|unlock/i).first()).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('locked-source.png'), fullPage: true });
  await page.getByRole('button', { name: /Unlock profile|Unlock source/, exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Unlock Locked source', exact: true });
  await dialog.getByLabel('Username', { exact: true }).fill(sourceCredentials.username);
  await dialog.getByLabel('Password', { exact: true }).fill(sourceCredentials.password);
  await dialog.getByRole('button', { name: 'Unlock profile', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
  await navigation(page, 'Offline source');
  await expect(page.getByRole('alert')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('offline-source.png'), fullPage: true });
  await expect(page.getByRole('button', { name: /Retry|Refresh/, exact: true }).first()).toBeVisible();
  await navigation(page, 'Unsupported source');
  await expect(page.getByRole('alert')).toContainText(/listing|supported/i);
});

test('cached listing reports a source refresh failure and recovers', async ({ page }) => {
  try {
    await sourceState(page.request, { offline: true });
    await page.getByRole('button', { name: /Refresh folder|Refresh/, exact: true }).click();
    await expect(page.getByText(/cached|stale|source.*unavailable/i).first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
  } finally { await sourceState(page.request); }
  await page.getByRole('button', { name: /Refresh folder|Refresh/, exact: true }).click();
  await expect(page.getByRole('region', { name: 'Files', exact: true })).toHaveAttribute('aria-busy', 'false');
});

test('10,000 entries keep a bounded DOM and selection survives virtual scrolling', async ({ page }, testInfo) => {
  const started = performance.now();
  await page.getByRole('button', { name: 'Large', exact: true }).click();
  const table = page.getByRole('table', { name: 'Folder contents', exact: true });
  await expect(table).toHaveAttribute('aria-rowcount', '10001');
  const initialRenderMs = Math.round(performance.now() - started);
  const initialRenderedRows = await table.locator('tbody tr[aria-rowindex]').count();
  await writeFile(testInfo.outputPath('large-list.json'), JSON.stringify({ initialRenderMs, initialRenderedRows, listingEntries: 10000, initiallyLoadedEntries: 500 }, null, 2));
  await page.screenshot({ path: testInfo.outputPath('large-folder.png'), fullPage: true });
  const scroller = page.getByRole('region', { name: 'Files', exact: true });
  const first = scroller.getByRole('checkbox').first();
  const label = await first.getAttribute('aria-label');
  expect(label).toBeTruthy();
  await first.check();
  await scroller.focus();
  await scroller.evaluate((element) => { element.scrollTop = 6000; });
  await expect.poll(() => table.locator('tbody tr[aria-rowindex]').count()).toBeLessThan(60);
  await expect(page.getByRole('checkbox', { name: label!, exact: true })).not.toBeVisible();
  await scroller.evaluate((element) => { element.scrollTop = 0; });
  await expect(page.getByRole('checkbox', { name: label!, exact: true })).toBeChecked();
  await scroller.focus();
  await page.keyboard.press('Control+a');
  await expect(page.getByRole('checkbox', { name: label!, exact: true })).toBeChecked();
  await expect(page.getByText(/500 selected/)).toBeVisible();
  await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('café & snow 雪');
  await expect(page.getByRole('button', { name: 'café & snow 雪.txt', exact: true })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('large-unicode-filename.png'), fullPage: true });
  await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('long filename');
  await expect(page.getByRole('button', { name: /^long filename/ })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('large-long-filename.png'), fullPage: true });
});

test('grid modifier clicks and Space select without opening files', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  await page.getByRole('button', { name: 'mountain.png', exact: true }).click({ modifiers: ['Control'] });
  await expect(page.getByRole('checkbox', { name: 'Select mountain.png', exact: true })).toBeChecked();
  const second = page.getByRole('button', { name: 'sunset.jpg', exact: true });
  await second.focus();
  await page.keyboard.press('Space');
  await expect(page.getByRole('checkbox', { name: 'Select sunset.jpg', exact: true })).toBeChecked();
  await expect(page.getByRole('dialog')).not.toBeVisible();
});

test('virtual table keeps filenames full width across responsive columns', async ({ page }, testInfo) => {
  await page.getByRole('button', { name: 'Large', exact: true }).click();
  const scroller = page.getByRole('region', { name: 'Files', exact: true });
  const first = scroller.getByRole('checkbox').first();
  const selectedName = await first.getAttribute('aria-label');
  await first.check();
  for (const [width, view, columns] of [
    [390, 'Detailed view', 3], [1024, 'Detailed view', 5], [1440, 'Detailed view', 6],
    [1440, 'Compact view', 4], [390, 'Compact view', 3], [1440, 'Detailed view', 6],
  ] as const) {
    await page.setViewportSize({ width, height: 844 });
    await page.getByRole('button', { name: view, exact: true }).click();
    await expect(page.getByRole('checkbox', { name: selectedName!, exact: true })).toBeChecked();
    await scroller.focus();
    await scroller.evaluate((element) => { element.scrollTop = 6000; });
    await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(6000);
    await expect.poll(() => scroller.locator('th:visible').count()).toBe(columns);
    await expect.poll(() => scroller.locator('table').evaluate((table) => {
      const visibleWidth = [...table.querySelectorAll('th')].reduce((sum, cell) => sum + cell.getBoundingClientRect().width, 0);
      return Math.abs(table.getBoundingClientRect().width - visibleWidth);
    })).toBeLessThan(2);
    const nameWidth = await scroller.locator('th.name-cell').evaluate((cell) => cell.getBoundingClientRect().width);
    const tableWidth = await scroller.locator('table').evaluate((table) => table.getBoundingClientRect().width);
    expect(nameWidth).toBeGreaterThan(tableWidth * 0.5);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
    if (width === 390 && view === 'Detailed view')
      await page.screenshot({ path: testInfo.outputPath('phone-large-full-width.png'), fullPage: true });
    await scroller.evaluate((element) => { element.scrollTop = 0; });
    await expect(page.getByRole('checkbox', { name: selectedName!, exact: true })).toBeChecked();
  }
});

test('browser history restores loaded pages and deep scroll position', async ({ page }) => {
  await page.getByRole('button', { name: 'Large', exact: true }).click();
  const scroller = page.getByRole('region', { name: 'Files', exact: true });
  await expect(page.getByText('500 loaded of 10,000', { exact: true })).toBeVisible();
  await scroller.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect(page.getByText('1,000 loaded of 10,000', { exact: true })).toBeVisible();
  await scroller.evaluate((element) => { element.scrollTop = 40000; });
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(39000);
  await page.getByRole('button', { name: 'Parent folder', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Large', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByText('1,000 loaded of 10,000', { exact: true })).toBeVisible();
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(39000);
  const selected = scroller.getByRole('checkbox').first();
  const selectedName = await selected.getAttribute('aria-label');
  await selected.check();
  const detailedScroll = await scroller.evaluate((element) => element.scrollTop);
  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  await expect(page.getByText('1 selected', { exact: true })).toBeVisible();
  await scroller.evaluate((element) => { element.scrollTop = 100; });
  await expect.poll(() => page.evaluate(() => history.state.scroll as number)).toBe(100);
  await page.goBack();
  await expect(page.getByRole('table', { name: 'Folder contents', exact: true })).toBeVisible();
  await expect(page.getByText('1,000 loaded of 10,000', { exact: true })).toBeVisible();
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(detailedScroll);
  await expect(page.getByRole('checkbox', { name: selectedName!, exact: true })).toBeChecked();
  await page.goForward();
  await expect(page.locator('.virtual-grid')).toBeVisible();
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(100);
  await expect(page.getByText('1 selected', { exact: true })).toBeVisible();
});

test('profile creation tests an actual synthetic listing', async ({ page }, testInfo) => {
  const info = await fixtureInfo(page.request);
  const name = `Browser profile ${testInfo.project.name}`;
  await page.getByRole('button', { name: 'Add profile', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Add profile', exact: true });
  await dialog.getByLabel('Name', { exact: true }).fill(name);
  await dialog.getByLabel('Root URL', { exact: true }).fill(info.profiles.library.root_url);
  await dialog.getByText('Advanced connection settings', { exact: true }).click();
  await dialog.getByRole('checkbox', { name: 'Allow insecure HTTP', exact: true }).check();
  let id = '';
  try {
    const saved = page.waitForResponse((response) => response.url().endsWith('/api/v1/profiles') && response.request().method() === 'POST');
    await dialog.getByRole('button', { name: 'Save and test', exact: true }).click();
    const response = await saved;
    expect(response.status()).toBe(201);
    id = (await response.json() as { id: string }).id;
    await expect(page.getByRole('dialog').getByRole('status')).toContainText('listing detected');
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.getByRole('button', { name: 'Close dialog', exact: true }).click();
  } finally {
    if (id) {
      const session = await page.request.get('/api/v1/auth/session');
      const csrf = (await session.json() as { csrf_token: string }).csrf_token;
      expect((await page.request.delete(`/api/v1/profiles/${id}`, { headers: mutationHeaders(csrf) })).status()).toBe(204);
    }
  }
});

test('responsive light and dark layouts remain accessible', async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const session = await page.request.get('/api/v1/auth/session');
  const csrf = (await session.json() as { csrf_token: string }).csrf_token;
  for (const theme of ['light', 'dark']) {
    const settings = await page.request.get('/api/v1/preferences');
    const preferences: Record<string, unknown> = await settings.json();
    expect((await page.request.put('/api/v1/preferences', { headers: mutationHeaders(csrf), data: { ...preferences, theme } })).status()).toBe(200);
    for (const width of [1440, 1024, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.reload();
      await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
      await expect(page.locator('.app')).toHaveAttribute('data-theme', theme);
      await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
      await page.screenshot({ path: testInfo.outputPath(`${theme}-${width}.png`), fullPage: true });
      const colors = await page.locator('.workspace').evaluate((element) => ({ theme: document.querySelector('.app')?.getAttribute('data-theme'), rootPanel: getComputedStyle(document.querySelector('.app')!).getPropertyValue('--panel'), background: getComputedStyle(element).backgroundColor, panel: getComputedStyle(element).getPropertyValue('--panel'), color: getComputedStyle(element).color, headingColor: getComputedStyle(element.querySelector('h1')!).color }));
      await writeFile(testInfo.outputPath(`${theme}-${width}-colors.json`), JSON.stringify(colors, null, 2));
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    }
  }
});

test('appearance choices persist and account screens remain accessible', async ({ page }) => {
  const appearance = () => page.getByRole('button', { name: /^(Open appearance|Appearance)$/ });
  await appearance().click();
  const dialog = page.getByRole('dialog', { name: 'Appearance', exact: true });
  await dialog.getByRole('radio', { name: 'Light', exact: true }).check();
  await dialog.getByRole('radio', { name: 'Teal', exact: true }).check();
  await dialog.getByRole('checkbox', { name: 'Compact rows', exact: true }).check();
  await expect(dialog.getByRole('checkbox', { name: 'Compact rows', exact: true })).toBeEnabled();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await dialog.getByRole('button', { name: 'Done', exact: true }).click();
  await page.reload();
  await appearance().click();
  await expect(dialog.getByRole('radio', { name: 'Light', exact: true })).toBeChecked();
  await expect(dialog.getByRole('radio', { name: 'Teal', exact: true })).toBeChecked();
  await expect(dialog.getByRole('checkbox', { name: 'Compact rows', exact: true })).toBeChecked();
  await page.keyboard.press('Escape');
  await expect(appearance()).toBeFocused();
  for (const name of ['Settings', 'Overview']) {
    await navigation(page, name);
    await expect(page.getByRole('heading', { name: name === 'Overview' ? 'Your folders' : name, exact: true })).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  }
});

test('keyboard access, focus restoration and 200 percent reflow', async ({ page }, testInfo) => {
  await page.keyboard.press('/');
  await expect(page.getByRole('textbox', { name: 'Filter names', exact: true })).toBeFocused();
  await page.getByRole('button', { name: 'Details for readme.txt', exact: true }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', { name: 'Details for readme.txt', exact: true })).toBeFocused();
  await page.setViewportSize({ width: 1440, height: 900 });
  const before = await page.evaluate(() => ({ width: innerWidth, ratio: devicePixelRatio }));
  for (let step = 0; step < 5; step++) await page.keyboard.press('Control+Equal');
  const after = await page.evaluate(() => ({ width: innerWidth, ratio: devicePixelRatio }));
  const zoomWorked = after.ratio / before.ratio >= 1.9 && after.width <= before.width / 1.9;
  testInfo.annotations.push({ type: 'zoom', description: zoomWorked ? 'Native keyboard zoom reached at least 200%.' : 'Headless keyboard zoom unavailable; checked 200% equivalent reflow by halving the CSS viewport. Native browser zoom remains unverified.' });
  if (zoomWorked) {
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.screenshot({ path: testInfo.outputPath('native-200-percent-zoom.png'), fullPage: true });
  }
  await page.keyboard.press('Control+Digit0');
  await page.setViewportSize({ width: 720, height: 450 });
  await expect(page.getByRole('button', { name: 'readme.txt', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(720);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('200-percent-equivalent-reflow.png'), fullPage: true });
});
