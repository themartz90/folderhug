import { expect, test } from '@playwright/test';
import { expireSessions, fixtureInfo, navigation, openLibrary, ownerPassword } from './fixture';

let pageErrors: string[] = [];
test.beforeEach(async ({ page }) => {
  pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await openLibrary(page);
});
test.afterEach(() => expect(pageErrors).toEqual([]));

test('pending name filter survives an immediate type change', async ({ page }) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('mountain');
  await page.getByRole('combobox', { name: 'File type', exact: true }).selectOption('image');
  await expect(page.getByRole('textbox', { name: 'Filter names', exact: true })).toHaveValue(
    'mountain',
  );
  await expect(page).toHaveURL(/name=mountain/);
  await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'sunset.jpg', exact: true })).not.toBeVisible();
});

for (const action of ['sort', 'more filters'] as const)
  test(`pending name merges with ${action}`, async ({ page }) => {
    await page.getByRole('button', { name: 'Media', exact: true }).click();
    await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('mountain');
    if (action === 'sort')
      await page.getByRole('combobox', { name: 'Sort by', exact: true }).selectOption('size');
    else {
      await page.getByRole('button', { name: 'More filters', exact: true }).click();
      await page.getByLabel('Extensions, separated by commas', { exact: true }).fill('png');
      await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
      await expect(page).toHaveURL(/extensions=png/);
    }
    await expect(page.getByRole('textbox', { name: 'Filter names', exact: true })).toHaveValue(
      'mountain',
    );
    await expect(page).toHaveURL(/name=mountain/);
    await expect(page.getByRole('button', { name: 'mountain.png', exact: true })).toBeVisible();
  });

for (const action of ['clear', 'folder', 'profile', 'unmount'] as const)
  test(`pending name is invalidated by ${action}`, async ({ page }) => {
    await page.getByRole('button', { name: 'Media', exact: true }).click();
    if (action === 'clear')
      await page.getByRole('combobox', { name: 'File type', exact: true }).selectOption('image');
    await page.getByRole('textbox', { name: 'Filter names', exact: true }).fill('stale');
    if (action === 'clear')
      await page.getByRole('button', { name: 'Clear filters', exact: true }).click();
    else if (action === 'folder')
      await page.getByRole('button', { name: 'Synthetic library', exact: true }).last().click();
    else await navigation(page, action === 'profile' ? 'Offline source' : 'Server queue');
    // Wait past the actual debounce to detect a stale route mutation.
    await page.waitForTimeout(400);
    await expect(page).not.toHaveURL(/name=stale/);
    if (action === 'clear' || action === 'folder')
      await expect(page.getByRole('textbox', { name: 'Filter names', exact: true })).toHaveValue(
        '',
      );
    else if (action === 'unmount') await expect(page).toHaveURL(/page=downloads/);
  });

test('partial batch retries a lost accepted response with the same key and settings', async ({
  page,
}) => {
  const before = (await (await page.request.get('/api/v1/downloads?limit=100')).json()) as {
    downloads: { id: string }[];
  };
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await page.getByRole('checkbox', { name: 'Select sunset.jpg', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  const requests: { body: string | null; key: string | undefined }[] = [];
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue();
      return;
    }
    requests.push({
      body: route.request().postData(),
      key: route.request().headers()['idempotency-key'],
    });
    const response = await route.fetch();
    expect(response.ok()).toBeTruthy();
    if (requests.length === 2) await route.abort('failed');
    else await route.fulfill({ response });
  });
  await page.getByRole('button', { name: 'Queue files', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('1 already accepted');
  await expect(page.getByRole('combobox', { name: /^Destination/ })).toBeDisabled();
  await page.getByText('Transfer settings', { exact: true }).click();
  await expect(page.getByRole('combobox', { name: /^Connections per file/ })).toBeDisabled();
  await page.getByRole('button', { name: 'Queue files', exact: true }).click();
  await expect(
    page.getByRole('status').filter({ hasText: '2 files queued on the server.' }),
  ).toBeVisible();
  expect(requests).toHaveLength(3);
  expect(requests[2]).toEqual(requests[1]);
  expect(requests[0]?.key).not.toBe(requests[1]?.key);
  const after = (await (await page.request.get('/api/v1/downloads?limit=100')).json()) as {
    downloads: { id: string }[];
  };
  expect(
    after.downloads.filter((job) => !before.downloads.some((old) => old.id === job.id)),
  ).toHaveLength(2);
});

for (const sessionEnd of ['expiry', 'logout'] as const)
  test(`a pending accepted transfer cannot continue after ${sessionEnd} and sign in`, async ({
    page,
  }) => {
    await page.getByRole('button', { name: 'Media', exact: true }).click();
    await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
    await page.getByRole('checkbox', { name: 'Select sunset.jpg', exact: true }).check();
    await page.getByRole('button', { name: 'Save to server', exact: true }).click();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let accepted!: () => void;
    const acceptedResponse = new Promise<void>((resolve) => {
      accepted = resolve;
    });
    let submissions = 0;
    await page.route('**/api/v1/downloads', async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue();
        return;
      }
      submissions++;
      const response = await route.fetch();
      accepted();
      await gate;
      await route.fulfill({ response });
    });
    try {
      await page.getByRole('button', { name: 'Queue files', exact: true }).click();
      await acceptedResponse;
      await page.goBack();
      if (sessionEnd === 'expiry') {
        await expireSessions(page.request);
        await page.evaluate(() => window.dispatchEvent(new Event('online')));
      } else await page.getByRole('button', { name: 'Sign out', exact: true }).click();
      await expect(page.getByLabel('Owner password', { exact: true })).toBeVisible();
      await page.getByLabel('Owner password', { exact: true }).fill(ownerPassword);
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await navigation(page, 'Synthetic library');
      await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
      release();
      await page.waitForTimeout(400);
      expect(submissions).toBe(1);
      await expect(
        page.getByRole('dialog', { name: 'Find files', exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole('status').filter({ hasText: 'queued on the server' }),
      ).not.toBeVisible();
    } finally {
      release();
    }
  });

test('accepted transfer finishes without closing a replacement scan dialog', async ({ page }) => {
  const before = (await (await page.request.get('/api/v1/downloads?limit=100')).json()) as {
    downloads: { id: string }[];
  };
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await page.getByRole('checkbox', { name: 'Select sunset.jpg', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let accepted!: () => void;
  const firstAccepted = new Promise<void>((resolve) => {
    accepted = resolve;
  });
  const submitted: string[] = [];
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue();
      return;
    }
    submitted.push(route.request().postDataJSON().entry_id as string);
    const response = await route.fetch();
    if (submitted.length === 1) {
      accepted();
      await gate;
    }
    await route.fulfill({ response });
  });
  try {
    await page.getByRole('button', { name: 'Queue files', exact: true }).click();
    await firstAccepted;
    await page.goBack();
    await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
    const replacement = page.getByRole('dialog', {
      name: 'Find files',
      exact: true,
    });
    await replacement.getByText('Scan limits and exclusions', { exact: true }).click();
    await replacement.getByLabel('Folder depth', { exact: true }).fill('3');
    release();
    await expect.poll(() => submitted.length).toBe(2);
    await expect(
      page.getByRole('status').filter({ hasText: '2 files queued on the server.' }),
    ).toBeVisible();
    await expect(replacement).toBeVisible();
    await expect(replacement.getByLabel('Folder depth', { exact: true })).toHaveValue('3');
    const after = (await (await page.request.get('/api/v1/downloads?limit=100')).json()) as {
      downloads: { id: string }[];
    };
    expect(
      after.downloads.filter((job) => !before.downloads.some((old) => old.id === job.id)),
    ).toHaveLength(2);
  } finally {
    release();
  }
});

test('malformed date links keep the filter dialog usable', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  const info = await fixtureInfo(page.request);
  await page.goto(
    `/#page=files&profile=${info.profiles.library.id}&modified_after=invalid&modified_before=invalid`,
  );
  await page.getByRole('button', { name: 'More filters', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Folder filters', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
  await page.getByRole('button', { name: 'Clear filters', exact: true }).last().click();
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
});

test('valid date filters keep their local values after applying and reopening', async ({
  page,
}) => {
  await page.getByRole('button', { name: 'More filters', exact: true }).click();
  await page.getByLabel('Modified after', { exact: true }).fill('2026-09-01T12:35');
  await page.getByLabel('Modified before', { exact: true }).fill('2026-09-20T21:10');
  await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
  await page.getByRole('button', { name: 'More filters', exact: true }).click();
  await expect(page.getByLabel('Modified after', { exact: true })).toHaveValue('2026-09-01T12:35');
  await expect(page.getByLabel('Modified before', { exact: true })).toHaveValue('2026-09-20T21:10');
});

test('a lost scan response retries the original scan body and key', async ({ page }) => {
  await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
  const requests: {
    key: string | undefined;
    body: string | null;
    id: string;
  }[] = [];
  await page.route('**/api/v1/profiles/*/scans', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    expect(response.ok()).toBeTruthy();
    const job = (await response.json()) as { id: string };
    requests.push({
      key: route.request().headers()['idempotency-key'],
      body: route.request().postData(),
      id: job.id,
    });
    if (requests.length === 1) await route.abort('failed');
    else await route.fulfill({ response });
  });
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Retry uses the same request settings');
  await page.getByText('Scan limits and exclusions', { exact: true }).click();
  await expect(page.getByLabel('Folder depth', { exact: true })).toBeDisabled();
  await page.getByRole('dialog', { name: 'Find files', exact: true }).getByRole('button', { name: 'Find files', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Search results', exact: true })).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
});

test('a background batch failure reports uncertain acceptance without closing the replacement', async ({
  page,
}) => {
  await page.getByRole('button', { name: 'Media', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select mountain.png', exact: true }).check();
  await page.getByRole('checkbox', { name: 'Select sunset.jpg', exact: true }).check();
  await page.getByRole('button', { name: 'Save to server', exact: true }).click();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let accepted!: () => void;
  const acceptedResponse = new Promise<void>((resolve) => {
    accepted = resolve;
  });
  let requests = 0;
  await page.route('**/api/v1/downloads', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue();
      return;
    }
    requests++;
    const response = await route.fetch();
    if (requests === 1) {
      accepted();
      await gate;
      await route.fulfill({ response });
    } else await route.abort('failed');
  });
  try {
    await page.getByRole('button', { name: 'Queue files', exact: true }).click();
    await acceptedResponse;
    await page.goBack();
    await page.getByRole('button', { name: 'Find files', exact: true }).last().click();
    release();
    await expect(
      page.getByRole('status').filter({
        hasText: '1 confirmed accepted. Check Server queue before trying again',
      }),
    ).toBeVisible();
    expect(requests).toBe(2);
    await expect(page.getByRole('dialog', { name: 'Find files', exact: true })).toBeVisible();
  } finally {
    release();
  }
});
