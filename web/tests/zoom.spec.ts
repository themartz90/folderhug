import { chromium, expect, test, type BrowserContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { mkdir, mkdtemp, realpath, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { fixtureInfo, fixtureOrigin, signIn } from './fixture';

type ZoomWorker = typeof globalThis & {
  chrome: {
    tabs: {
      query: (query: { url: string }) => Promise<{ id?: number; url?: string }[]>;
      setZoom: (tabId: number, factor: number) => Promise<void>;
      getZoom: (tabId: number) => Promise<number>;
    };
  };
};

async function removeTestProfile(temporaryRoot: string, userDataDir: string) {
  const resolved = await realpath(userDataDir);
  const relative = path.relative(temporaryRoot, resolved);
  if (path.isAbsolute(relative) || relative.startsWith('..') || path.dirname(relative) !== '.' || !path.basename(relative).startsWith('folderhug-zoom-')) {
    throw new Error('Refusing to remove a browser profile outside the test temporary directory.');
  }
  await rm(resolved, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 });
}

test('native 200% browser zoom keeps browsing and preview controls usable', async ({ browserName }, testInfo) => {
  test.skip(browserName !== 'chromium', 'The tabs zoom API is specific to Chromium extensions.');
  const temporaryRoot = await realpath(tmpdir());
  const userDataDir = await mkdtemp(path.join(temporaryRoot, 'folderhug-zoom-'));
  let context: BrowserContext | undefined;
  try {
    const extension = fileURLToPath(new URL('./zoom-extension', import.meta.url));
    context = await chromium.launchPersistentContext(userDataDir, {
      channel: 'chromium',
      headless: true,
      baseURL: fixtureOrigin,
      viewport: { width: 1440, height: 900 },
      args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
    });
    const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker');
    const page = context.pages()[0] ?? await context.newPage();
    await signIn(page);
    const fixture = await fixtureInfo(page.request);
    await page.goto(`/#page=files&profile=${encodeURIComponent(fixture.profiles.library.id)}&directory=root&view=detailed`);
    await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
    const before = await page.evaluate(() => ({ width: innerWidth, height: innerHeight, ratio: devicePixelRatio }));
    expect(before.width).toBe(1440);
    expect(before.height).toBe(900);
    const zoom = await worker.evaluate(async (origin) => {
      const tabs = (globalThis as ZoomWorker).chrome.tabs;
      const [tab] = await tabs.query({ url: `${origin}/*` });
      if (tab?.id === undefined) throw new Error('The fixture tab was not found.');
      await tabs.setZoom(tab.id, 2);
      return tabs.getZoom(tab.id);
    }, fixtureOrigin);
    expect(zoom).toBe(2);
    await expect.poll(() => page.evaluate(() => innerWidth)).toBe(before.width / 2);
    await expect.poll(() => page.evaluate(() => innerHeight)).toBe(before.height / 2);
    await expect.poll(() => page.evaluate(() => devicePixelRatio)).toBe(before.ratio * 2);
    const after = await page.evaluate(() => ({ width: innerWidth, height: innerHeight, ratio: devicePixelRatio }));
    await testInfo.attach('native-browser-zoom', { body: JSON.stringify({ zoom, before, after }, null, 2), contentType: 'application/json' });

    await expect(page.locator('.sidebar')).not.toBeVisible();
    const menu = page.getByRole('button', { name: 'Toggle sidebar', exact: true });
    await expect(menu).toBeInViewport();
    await menu.click();
    const navigation = page.getByRole('dialog', { name: 'Profiles and navigation', exact: true });
    await expect(navigation).toBeVisible();
    await navigation.getByRole('button', { name: 'Synthetic library', exact: true }).click();
    await expect(navigation).not.toBeVisible();

    const filter = page.getByRole('textbox', { name: 'Filter names', exact: true });
    await expect(filter).toBeInViewport();
    await filter.fill('readme');
    const file = page.getByRole('button', { name: 'readme.txt', exact: true });
    await expect(file).toBeVisible();
    await expect(page.getByRole('button', { name: 'Media', exact: true })).not.toBeVisible();
    await file.click();
    const preview = page.getByRole('dialog', { name: 'readme.txt', exact: true });
    await expect(preview).toBeVisible();
    await expect(preview.getByRole('button', { name: 'Close dialog', exact: true })).toBeInViewport();
    await preview.getByRole('button', { name: 'Download', exact: true }).scrollIntoViewIfNeeded();
    await expect(preview.getByRole('button', { name: 'Download', exact: true })).toBeInViewport();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    const screenshot = fileURLToPath(new URL('../../.local/c6-ui/native-200-percent.png', import.meta.url));
    await mkdir(path.dirname(screenshot), { recursive: true });
    await page.screenshot({ path: screenshot });
    await testInfo.attach('native-200-percent', { path: screenshot, contentType: 'image/png' });
    await page.keyboard.press('Escape');
    await expect(preview).not.toBeVisible();
    await expect(file).toBeFocused();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  } finally {
    try { await context?.close(); }
    finally { await removeTestProfile(temporaryRoot, userDataDir); }
  }
});
