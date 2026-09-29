const keys = {
  find: ['folderhug.find', 'http-wrapper.find'],
  location: ['folderhug.location', 'http-wrapper.location'],
  gridSize: ['folderhug.grid-size', 'httpwrapper.grid-size'],
} as const;

type Key = keyof typeof keys;
type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

export function readStoredValue(store: Store, key: Key): string | null {
  const [currentKey, oldKey] = keys[key];
  const current = store.getItem(currentKey);
  if (current !== null) {
    try { store.removeItem(oldKey); } catch { /* Browser storage may be read-only. */ }
    return current;
  }
  const old = store.getItem(oldKey);
  if (old === null) return null;
  try {
    store.setItem(currentKey, old);
    store.removeItem(oldKey);
  } catch {
    /* The old value remains usable when browser storage is read-only. */
  }
  return old;
}

export function writeStoredValue(store: Store, key: Key, value: string): void {
  const [currentKey, oldKey] = keys[key];
  store.setItem(currentKey, value);
  try { store.removeItem(oldKey); } catch { /* The current value is saved. */ }
}

export function removeStoredValue(store: Store, key: Key): void {
  for (const name of keys[key]) {
    try { store.removeItem(name); } catch { /* Browser storage may be unavailable. */ }
  }
}
