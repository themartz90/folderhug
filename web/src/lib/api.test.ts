import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, currentSession, endpoint, refreshSession, setSession } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
  setSession(null);
});
describe('API client', () => {
  it('keeps opaque IDs and filters within a same-origin request', () => {
    expect(endpoint('/search', { q: '日本語 & #', min_size: 0, show_folders: false })).toBe(
      '/api/v1/search?q=%E6%97%A5%E6%9C%AC%E8%AA%9E+%26+%23&min_size=0&show_folders=false',
    );
    expect(() => endpoint('//outside.test')).toThrow();
  });
  it('sends CSRF and a stable operation key without URL credentials', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('{}'));
    vi.stubGlobal('fetch', fetcher);
    setSession({
      authenticated: true,
      csrf_token: 'synthetic-csrf',
      expires_at: '2030-01-01T00:00:00Z',
    });
    await api('/downloads', { method: 'POST', body: { entry_id: 'entry' }, key: 'operation' });
    const [url, options] = fetcher.mock.calls[0]!;
    expect(url).toBe('/api/v1/downloads');
    expect(options.headers.get('X-CSRF-Token')).toBe('synthetic-csrf');
    expect(options.headers.get('Idempotency-Key')).toBe('operation');
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it('expires the owner session only for Folderhug authentication errors', async () => {
    const onExpired = vi.fn();
    setSession(null, onExpired);
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          new Response('{"code":"upstream_auth_required","message":"Unlock source"}', {
            status: 502,
          }),
        )
        .mockResolvedValueOnce(new Response('{}', { status: 401 })),
    );
    await expect(api('/profiles/source/browse')).rejects.toBeInstanceOf(ApiError);
    expect(onExpired).not.toHaveBeenCalled();
    await expect(api('/profiles')).rejects.toBeInstanceOf(ApiError);
    expect(onExpired).toHaveBeenCalledOnce();
  });
  it('ignores old requests after a different session signs in', async () => {
    let complete!: (response: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise<Response>((resolve) => (complete = resolve))),
    );
    const request = api('/profiles');
    const onExpired = vi.fn();
    setSession(null, onExpired);
    complete(new Response('{}', { status: 401 }));
    await expect(request).rejects.toMatchObject({ name: 'AbortError' });
    expect(onExpired).not.toHaveBeenCalled();
  });
  it('refreshes with CSRF while keeping the active session generation', async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response(JSON.stringify({
      authenticated: true, csrf_token: 'renewed-csrf', expires_at: '2030-01-02T00:00:00Z',
    })));
    vi.stubGlobal('fetch', fetcher);
    setSession({ authenticated: true, csrf_token: 'original-csrf', expires_at: '2030-01-01T00:00:00Z' });
    const generation = currentSession();
    await refreshSession();
    expect(currentSession()).toBe(generation);
    const [url, options] = fetcher.mock.calls[0]!;
    expect(url).toBe('/api/v1/auth/refresh');
    expect(options.headers.get('X-CSRF-Token')).toBe('original-csrf');
    await api('/preferences', { method: 'PUT', body: {} });
    expect(fetcher.mock.calls[1]![1].headers.get('X-CSRF-Token')).toBe('renewed-csrf');
  });
  it('cannot apply a late refresh after a different session starts', async () => {
    let complete!: (response: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => (complete = resolve))));
    setSession({ authenticated: true, csrf_token: 'old', expires_at: '2030-01-01T00:00:00Z' });
    const pending = refreshSession();
    setSession({ authenticated: true, csrf_token: 'new', expires_at: '2030-01-01T00:00:00Z' });
    complete(new Response(JSON.stringify({ authenticated: true, csrf_token: 'stale', expires_at: '2030-01-01T00:00:00Z' })));
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  });
});
