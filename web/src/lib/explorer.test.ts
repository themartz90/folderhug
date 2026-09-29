import { afterEach, describe, expect, it, vi } from 'vitest';
import { filenameParts, readRoute, routeHash, selectionRange } from './explorer';
import { localDate, type Entry } from './api';

afterEach(() => vi.unstubAllGlobals());
describe('route dates', () => {
  it('handles a date that exceeds the Date range during local-time conversion', () => {
    const offset = vi.spyOn(Date.prototype, 'getTimezoneOffset').mockReturnValue(-1440);
    try {
      expect(localDate('+275760-09-13T00:00:00.000Z')).toBe('');
    } finally {
      offset.mockRestore();
    }
  });
  it('drops malformed dates from either filter without dropping other filters', () => {
    vi.stubGlobal('location', {
      hash: '#name=report&modified_after=invalid&modified_before=2026-99-99T00:00:00Z',
    });
    vi.stubGlobal('history', { state: null });
    expect(readRoute().filters).toEqual({ name: 'report' });
    expect(localDate('invalid')).toBe('');
    expect(localDate('2026-99-99T00:00:00Z')).toBe('');
  });
  it('round trips valid zoned dates and formats local wall time', () => {
    const after = '2026-09-20T14:35:00+02:00';
    const before = '2026-09-21T16:00:00Z';
    vi.stubGlobal('location', {
      hash: `#modified_after=${encodeURIComponent(after)}&modified_before=${before}`,
    });
    vi.stubGlobal('history', { state: null });
    const route = readRoute();
    expect(route.filters).toEqual({
      modified_after: after,
      modified_before: before,
    });
    expect(new URLSearchParams(routeHash(route).slice(1)).get('modified_after')).toBe(after);
    const date = new Date(after);
    const pad = (n: number) => String(n).padStart(2, '0');
    expect(localDate(after)).toBe(
      `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`,
    );
  });
});

const entry = (id: string, name = `${id}.txt`): Entry => ({
  id,
  name,
  profile_id: 'synthetic',
  parent_id: 'root',
  escaped_path: name,
  kind: 'file',
  category: 'document',
  extension: 'txt',
  size: null,
  size_exact: false,
  modified_at: null,
  modified_text: null,
  timezone_known: false,
  provenance: 'nginx',
});

describe('explorer selection', () => {
  it('extends a reverse range without dropping a hidden selection', () => {
    const entries = ['a', 'b', 'c', 'd'].map((id) => entry(id));
    const hidden = entry('hidden');
    const selected = selectionRange(entries, { hidden }, 'b', 'd', true);
    expect(Object.keys(selected).sort()).toEqual(['b', 'c', 'd', 'hidden']);
  });
  it('falls back to a single item after the range anchor is filtered out', () => {
    const a = entry('a');
    const result = selectionRange([a], { hidden: entry('hidden') }, 'a', 'absent', true);
    expect(Object.keys(result).sort()).toEqual(['a', 'hidden']);
    expect(selectionRange([a], result, 'a', 'a', false)).toEqual({
      hidden: entry('hidden'),
    });
  });
  it('does not change the visible spelling of Unicode filenames or extensions', () => {
    const file = { ...entry('1', '日本語 résumé.TXT'), extension: 'txt' };
    expect(filenameParts(file)).toEqual(['日本語 résumé', '.TXT']);
    expect(filenameParts({ ...file, name: 'extensionless', extension: null })).toEqual([
      'extensionless',
      '',
    ]);
  });
});
