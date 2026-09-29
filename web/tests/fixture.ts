import { expect, type APIRequestContext, type Page } from '@playwright/test';

// These credentials are public synthetic test data, never a configured source.
export const ownerPassword = 'synthetic owner password';
export const sourceCredentials = { username: 'fixture-user', password: 'fixture-password' };
export const fixtureOrigin = 'http://127.0.0.1:4173';
const controlOrigin = 'http://127.0.0.1:4174';
const controlHeaders = { 'X-Fixture-Control': 'synthetic-local-control' };

export interface FixtureProfile {
  id: string;
  name: string;
  root_url: string;
  auth_type: string;
}

export interface FixtureInfo {
  origin: string;
  profiles: Record<'library' | 'locked' | 'offline' | 'unsupported', FixtureProfile>;
}

export async function fixtureInfo(request: APIRequestContext): Promise<FixtureInfo> {
  const response = await request.get(`${controlOrigin}/info`, { headers: controlHeaders });
  expect(response.ok()).toBeTruthy();
  return response.json() as Promise<FixtureInfo>;
}

export async function sourceState(request: APIRequestContext, state: { offline?: boolean; delay_ms?: number } = {}) {
  const response = await request.post(`${controlOrigin}/source`, { headers: controlHeaders, data: state });
  expect(response.status()).toBe(204);
}

export async function expireSessions(request: APIRequestContext) {
  const response = await request.post(`${controlOrigin}/expire-sessions`, { headers: controlHeaders });
  expect(response.status()).toBe(204);
}

// API seeding shares the browser context's cookie jar and still passes the
// application's real password hashing, session cookie, Origin and CSRF guards.
export async function signIn(page: Page): Promise<string> {
  const response = await page.request.post('/api/v1/auth/login', {
    headers: { Origin: fixtureOrigin },
    data: { password: ownerPassword },
  });
  expect(response.status()).toBe(200);
  const session = await response.json() as { csrf_token: string };
  return session.csrf_token;
}

export function mutationHeaders(csrf: string): Record<string, string> {
  return { Origin: fixtureOrigin, 'X-CSRF-Token': csrf };
}

export async function resetPreferences(page: Page, csrf: string) {
  const response = await page.request.put('/api/v1/preferences', {
    headers: mutationHeaders(csrf),
    data: { theme: 'system', accent: 'blue', density: 'comfortable', profile_views: {}, filter_presets: [], profile_order: [], last_destination: { destination_id: 'server', path: '' }, destination_favorites: [] },
  });
  expect(response.status()).toBe(200);
}

export async function openLibrary(page: Page): Promise<string> {
  await sourceState(page.request);
  const csrf = await signIn(page);
  await resetPreferences(page, csrf);
  await page.goto('/');
  await navigation(page, 'Synthetic library');
  await expect(page.getByRole('button', { name: 'Media', exact: true })).toBeVisible();
  return csrf;
}

export async function navigation(page: Page, name: string) {
  const button = page.locator('.sidebar').getByRole('button', { name, exact: true, includeHidden: true });
  await expect(button).toBeAttached();
  if (!await button.isVisible()) {
    await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
    await page.getByRole('dialog', { name: 'Profiles and navigation', exact: true }).getByRole('button', { name, exact: true }).click();
    return;
  }
  await button.click();
}

export async function createProfile(page: Page, csrf: string, name: string): Promise<FixtureProfile> {
  const info = await fixtureInfo(page.request);
  const response = await page.request.post('/api/v1/profiles', {
    headers: mutationHeaders(csrf),
    data: { name, root_url: info.profiles.library.root_url, auth_type: 'none', allow_insecure_http: true },
  });
  expect(response.status()).toBe(201);
  return response.json() as Promise<FixtureProfile>;
}

export async function removeProfile(page: Page, csrf: string, profile: FixtureProfile) {
  const response = await page.request.delete(`/api/v1/profiles/${profile.id}`, { headers: mutationHeaders(csrf) });
  expect(response.status()).toBe(204);
}
