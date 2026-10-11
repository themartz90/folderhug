import { afterEach, expect, it, vi } from 'vitest';
import { randomUUID } from './uuid';

afterEach(() => { vi.unstubAllGlobals(); });

it('creates a version 4 UUID when the browser has no native randomUUID', () => {
  vi.stubGlobal('crypto', {
    getRandomValues: (bytes: Uint8Array) => bytes.fill(255),
  });
  expect(randomUUID()).toBe('ffffffff-ffff-4fff-bfff-ffffffffffff');
});

it('uses fresh randomness for each identifier', () => {
  const ids = Array.from({ length: 100 }, () => randomUUID());
  expect(new Set(ids).size).toBe(ids.length);
  for (const id of ids) {
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  }
});
