import type { components } from './api.generated';

export type Schema = components['schemas'];
export type Entry = Schema['Entry'];
export type Profile = Schema['Profile'];
export type Preferences = Schema['Preferences'];
export type Download = Schema['Download'];
export type Scan = Schema['Scan'];

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId = '',
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof DOMException && error.name === 'AbortError') return '';
  return 'Could not reach Folderhug. Check your connection and try again.';
}

export function endpoint(path: string, query: Record<string, unknown> = {}): string {
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('://')) {
    throw new Error('Expected an API path');
  }
  const values = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') values.set(key, String(value));
  }
  return `/api/v1${path}${values.size ? `?${values}` : ''}`;
}

export const idPath = (id: string) => encodeURIComponent(id);
export const contentPath = (entry: Entry) =>
  `/profiles/${idPath(entry.profile_id)}/entries/${idPath(entry.id)}/content`;

let csrf = '';
let expired: (() => void) | undefined;
let sessionGeneration = 0;
export const currentSession = () => sessionGeneration;
export function setSession(session: Schema['Session'] | null, onExpired?: () => void) {
  sessionGeneration++;
  csrf = session?.csrf_token ?? '';
  expired = onExpired;
}

// A refresh extends a remembered cookie without replacing the active session.
export async function refreshSession(): Promise<void> {
  const generation = sessionGeneration;
  const session = await api<Schema['Session']>('/auth/refresh', { method: 'POST' });
  requireCurrent(generation);
  csrf = session.csrf_token;
}

function requireCurrent(generation: number) {
  if (generation !== sessionGeneration) throw new DOMException('Session changed', 'AbortError');
}

async function failure(response: Response, generation: number): Promise<never> {
  let problem: Partial<Schema['ApiError']> = {};
  try {
    problem = await response.json();
  } catch {
    /* A proxy may return a non-JSON error. */
  }
  requireCurrent(generation);
  if (response.status === 401) expired?.();
  throw new ApiError(
    response.status,
    problem.code ?? 'unavailable',
    problem.message ?? `The request failed (${response.status}).`,
    problem.request_id ?? '',
  );
}

export async function api<T>(
  path: string,
  options: {
    method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
    body?: unknown;
    query?: Record<string, unknown>;
    signal?: AbortSignal;
    key?: string;
  } = {},
): Promise<T> {
  const method = options.method ?? 'GET';
  const generation = sessionGeneration;
  const headers = new Headers({ Accept: 'application/json' });
  if (method !== 'GET') headers.set('X-CSRF-Token', csrf);
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  if (options.key) headers.set('Idempotency-Key', options.key);
  const response = await fetch(endpoint(path, options.query), {
    method,
    headers,
    credentials: 'same-origin',
    cache: 'no-store',
    ...(options.body !== undefined ? { body: JSON.stringify(options.body) } : {}),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  requireCurrent(generation);
  if (!response.ok) return failure(response, generation);
  if (response.status === 204) return undefined as T;
  const value: T = await response.json();
  requireCurrent(generation);
  return value;
}

// Let the browser stream attachments to disk without a JavaScript-sized buffer.
export async function browserDownload(path: string): Promise<void> {
  const generation = sessionGeneration;
  const response = await fetch(endpoint(path), {
    method: 'HEAD',
    credentials: 'same-origin',
    cache: 'no-store',
  });
  requireCurrent(generation);
  if (!response.ok) return failure(response, generation);
  const link = document.createElement('a');
  link.href = endpoint(path);
  link.download = '';
  document.body.append(link);
  link.click();
  link.remove();
}

export function formatBytes(value: number | null | undefined): string {
  if (value == null) return 'Unknown size';
  if (value === 0) return '0 B';
  const unit = Math.max(0, Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4));
  return `${(value / 1024 ** unit).toLocaleString(undefined, { maximumFractionDigits: unit ? 1 : 0 })} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`;
}

export function formatDate(entry: Entry): string {
  if (!entry.modified_at || !entry.timezone_known) return entry.modified_text ?? 'Unknown date';
  return new Date(entry.modified_at).toLocaleString();
}

export function localDate(value?: string): string {
  if (!value) return '';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  if (!Number.isFinite(date.getTime())) return '';
  return date.toISOString().slice(0, 16);
}
