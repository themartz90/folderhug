import { describe, expect, it } from 'vitest';
import { ageUpdateStatus, canAutoShow, markReleaseSeen, releaseURL, seenKey } from './releases';
import type { Schema } from './api';

function storage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value); },
    removeItem: (key: string) => { values.delete(key); },
  };
}
const notes = { version: 'v1.2.3', auto_show: true, summary: 'Changes.', highlights: ['A fix.'] };

describe('release highlights', () => {
  it('opens an unseen installed release once and allows a different version', () => {
    const store = storage();
    expect(canAutoShow('v1.2.3', notes, store)).toBe(true);
    expect(markReleaseSeen('v1.2.3', store)).toBe(true);
    expect(canAutoShow('v1.2.3', notes, store)).toBe(false);
    expect(canAutoShow('v1.2.4', { ...notes, version: 'v1.2.4' }, store)).toBe(true);
    expect(store.getItem(seenKey('v1.2.3'))).toBe('seen');
  });
  it('does not automatically open dev, mismatched, absent or disabled notes', () => {
    const store = storage();
    expect(canAutoShow('dev', { ...notes, version: 'dev' }, store)).toBe(false);
    expect(canAutoShow('v1.2.4', notes, store)).toBe(false);
    expect(canAutoShow('v1.2.3', null, store)).toBe(false);
    expect(canAutoShow('v1.2.3', { ...notes, auto_show: false }, store)).toBe(false);
  });
  it('suppresses automatic opening when storage is blocked or read-only', () => {
    const blocked = { ...storage(), getItem: () => { throw new Error('blocked'); } };
    const readOnly = { ...storage(), setItem: () => { throw new Error('read-only'); } };
    expect(canAutoShow('v1.2.3', notes, blocked)).toBe(false);
    expect(canAutoShow('v1.2.3', notes, readOnly)).toBe(false);
    expect(markReleaseSeen('v1.2.3', readOnly)).toBe(false);
  });
  it('derives only official release links from supported tags', () => {
    expect(releaseURL('v1.2.3')).toBe('https://github.com/themartz90/folderhug/releases/tag/v1.2.3');
    expect(releaseURL('v1.2.3-rc.10')).toContain('/tag/v1.2.3-rc.10');
    expect(releaseURL('dev')).toBe('https://github.com/themartz90/folderhug/releases');
    for (const value of ['v01.2.3', 'v1.2.3?x=1', 'https://evil.test/', 'v1.2.3-rc.0']) {
      expect(releaseURL(value)).toBe('https://github.com/themartz90/folderhug/releases');
    }
  });
  it('expires an equal cached release while preserving an older positive update link', () => {
    const checkedAt = Date.parse('2026-10-16T12:00:00Z');
    const status: Schema['UpdateStatus'] = { state: 'current', latest: { version: 'v1.2.3', release_url: releaseURL('v1.2.3'), published_at: '2026-10-16T12:00:00Z' }, checked_at: new Date(checkedAt).toISOString(), stale: false, retry_at: null };
    expect(ageUpdateStatus(status, checkedAt + 11 * 60 * 60 * 1000)).toBe(status);
    expect(ageUpdateStatus(status, checkedAt + 12 * 60 * 60 * 1000)).toMatchObject({ state: 'unknown', stale: true });
    expect(ageUpdateStatus({ ...status, state: 'available' }, checkedAt + 13 * 60 * 60 * 1000)).toMatchObject({ state: 'available', stale: true, latest: status.latest });
  });
});
