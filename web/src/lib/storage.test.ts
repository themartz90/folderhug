import { describe, expect, it } from 'vitest';
import { readStoredValue, removeStoredValue, writeStoredValue } from './storage';

function store(initial: Record<string, string> = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (name: string) => values.get(name) ?? null,
    setItem: (name: string, value: string) => { values.set(name, value); },
    removeItem: (name: string) => { values.delete(name); },
  };
}

describe('browser storage name migration', () => {
  it.each([
    ['find', 'http-wrapper.find', 'folderhug.find'],
    ['location', 'http-wrapper.location', 'folderhug.location'],
    ['gridSize', 'httpwrapper.grid-size', 'folderhug.grid-size'],
  ] as const)('moves an existing %s value to the Folderhug key', (kind, oldName, newName) => {
    const browser = store({ [oldName]: 'saved' });
    expect(readStoredValue(browser, kind)).toBe('saved');
    expect(browser.getItem(newName)).toBe('saved');
    expect(browser.getItem(oldName)).toBeNull();
  });

  it('keeps a current value when an old value also exists', () => {
    const browser = store({ 'folderhug.grid-size': 'large', 'httpwrapper.grid-size': 'small' });
    expect(readStoredValue(browser, 'gridSize')).toBe('large');
    expect(browser.getItem('httpwrapper.grid-size')).toBeNull();
  });

  it('uses the old value if the browser refuses to save the migration', () => {
    const browser = store({ 'http-wrapper.location': 'saved' });
    browser.setItem = () => { throw new Error('Storage blocked'); };
    expect(readStoredValue(browser, 'location')).toBe('saved');
    expect(browser.getItem('http-wrapper.location')).toBe('saved');
  });

  it('writes the current key and clears both names when requested', () => {
    const browser = store({ 'http-wrapper.find': 'old' });
    writeStoredValue(browser, 'find', 'new');
    expect(browser.getItem('folderhug.find')).toBe('new');
    expect(browser.getItem('http-wrapper.find')).toBeNull();
    removeStoredValue(browser, 'find');
    expect(browser.getItem('folderhug.find')).toBeNull();
  });
});
