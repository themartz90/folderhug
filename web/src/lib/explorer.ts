import type { Preferences, Schema, Entry } from './api';

export type View = Preferences['profile_views'][string];
export type Route = {
  page: 'files' | 'overview' | 'search' | 'downloads' | 'settings';
  profile: string;
  directory: string;
  filters: Schema['PresetFilters'];
  view: View;
  scroll: number;
  loaded: number;
};
export const defaultView: View = {
  view: 'detailed',
  sort: 'name',
  direction: 'asc',
  folders_first: true,
};
export function readRoute(): Route {
  const query = new URLSearchParams(location.hash.slice(1));
  const page = query.get('page') ?? 'files';
  const filters: Schema['PresetFilters'] = {};
  for (const key of ['name', 'extensions'] as const)
    if (query.has(key)) filters[key] = query.get(key)!;
  for (const key of ['modified_after', 'modified_before'] as const) {
    const value = query.get(key);
    if (
      value &&
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
      Number.isFinite(Date.parse(value))
    )
      filters[key] = value;
  }
  const category = query.get('category');
  if (
    category &&
    ['folder', 'video', 'image', 'audio', 'document', 'archive', 'other'].includes(category)
  )
    filters.category = category as Schema['Category'];
  for (const key of ['min_size', 'max_size'] as const)
    if (query.has(key) && Number.isFinite(Number(query.get(key))) && Number(query.get(key)) >= 0)
      filters[key] = Number(query.get(key));
  if (query.get('show_folders') === 'false') filters.show_folders = false;
  return {
    page: ['files', 'overview', 'search', 'downloads', 'settings'].includes(page)
      ? (page as Route['page'])
      : page === 'scans' ? 'search'
      : 'files',
    profile: query.get('profile') ?? '',
    directory: query.get('directory') ?? 'root',
    filters,
    view: {
      view: ['detailed', 'compact', 'grid'].includes(query.get('view') ?? '')
        ? (query.get('view') as View['view'])
        : 'detailed',
      sort: ['name', 'size', 'modified', 'type'].includes(query.get('sort') ?? '')
        ? (query.get('sort') as View['sort'])
        : 'name',
      direction: query.get('direction') === 'desc' ? 'desc' : 'asc',
      folders_first: query.get('folders_first') !== 'false',
    },
    scroll: Number(history.state?.scroll) || 0,
    loaded: Math.min(100000, Math.max(0, Number(history.state?.loaded) || 0)),
  };
}
export function routeHash(route: Route): string {
  const q = new URLSearchParams({ page: route.page });
  if (route.profile) q.set('profile', route.profile);
  if (route.page === 'search') q.set('directory', route.directory);
  if (route.page === 'files') {
    q.set('directory', route.directory);
    Object.entries({ ...route.filters, ...route.view }).forEach(([key, value]) => {
      if (value !== undefined && value !== '') q.set(key, String(value));
    });
  }
  return `#${q}`;
}
export function selectionRange(
  entries: Entry[],
  previous: Record<string, Entry>,
  id: string,
  anchor: string,
  range: boolean,
): Record<string, Entry> {
  const result = { ...previous };
  const target = entries.findIndex((entry) => entry.id === id);
  const start = entries.findIndex((entry) => entry.id === anchor);
  if (target < 0) return result;
  if (range && start >= 0) {
    for (const entry of entries.slice(Math.min(start, target), Math.max(start, target) + 1))
      result[entry.id] = entry;
  } else if (result[id]) delete result[id];
  else result[id] = entries[target]!;
  return result;
}
export function filenameParts(entry: Entry): [string, string] {
  const ext = entry.extension ? `.${entry.extension}` : '';
  return ext && entry.name.toLowerCase().endsWith(ext.toLowerCase())
    ? [entry.name.slice(0, -ext.length), entry.name.slice(-ext.length)]
    : [entry.name, ''];
}
