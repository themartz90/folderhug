import type { Schema } from './api';

type ReleaseStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
const releases = 'https://github.com/themartz90/folderhug/releases';
const versionPattern = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$/;

export const seenKey = (version: string) => `folderhug.release-highlights.seen.${version}`;

export function releaseURL(version: string): string {
  return versionPattern.test(version) ? `${releases}/tag/${version}` : releases;
}

export function ageUpdateStatus(status: Schema['UpdateStatus'], now = Date.now()): Schema['UpdateStatus'] {
  if (!status.latest || status.stale) return status;
  const checked = Date.parse(status.checked_at ?? '');
  if (Number.isFinite(checked) && now - checked < 12 * 60 * 60 * 1000) return status;
  return { ...status, stale: true, state: status.state === 'available' ? 'available' : 'unknown' };
}

export function canAutoShow(version: string, notes: Schema['ReleaseHighlights'] | null, store?: ReleaseStorage): boolean {
  if (!versionPattern.test(version) || !notes?.auto_show || notes.version !== version) return false;
  try {
    const storage = store ?? window.localStorage;
    if (storage.getItem(seenKey(version)) === 'seen') return false;
    const probe = 'folderhug.release-highlights.storage-check';
    storage.setItem(probe, '1');
    storage.removeItem(probe);
    return true;
  } catch {
    return false;
  }
}

export function markReleaseSeen(version: string, store?: ReleaseStorage): boolean {
  try {
    (store ?? window.localStorage).setItem(seenKey(version), 'seen');
    return true;
  } catch {
    return false;
  }
}
