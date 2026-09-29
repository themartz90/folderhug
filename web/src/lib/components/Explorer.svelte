<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import {
    api,
    ApiError,
    endpoint,
    errorMessage,
    formatBytes,
    formatDate,
    idPath,
    type Entry,
    type Preferences,
    type Profile,
    type Schema,
  } from '../api';
  import { selectionRange, type Route, type View } from '../explorer';
  import FileList from './FileList.svelte';
  import Filters from './Filters.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import FileMark from '../ui/FileMark.svelte';
  import Icon from '../ui/Icon.svelte';
  let {
    profile,
    profileRevision,
    route,
    preferences,
    bookmarks,
    onroute,
    onsavepreferences,
    onprofile,
    onscan,
    ontransfer,
    onopen,
    onpin,
    onmenu,
    onnotice,
  }: {
    profileRevision: number;
    profile: Profile;
    route: Route;
    preferences: Preferences;
    bookmarks: Schema['Bookmark'][];
    onroute: (route: Route) => void;
    onsavepreferences: (patch: Partial<Preferences>) => Promise<void>;
    onprofile: (mode: 'edit' | 'unlock') => void;
    onscan: (profile: string, directory: string) => void;
    ontransfer: (entries: Entry[], mode: 'browser' | 'server') => void;
    onopen: (entry: Entry, siblings: Entry[]) => void;
    onpin: (profile: string, directory: string, name: string) => void;
    onmenu: () => void;
    onnotice: (message: string) => void;
  } = $props();
  let entries = $state<Entry[]>([]);
  let page = $state<Schema['BrowsePage'] | null>(null);
  let busy = $state(false);
  let error = $state('');
  let code = $state('');
  let requestId = $state('');
  let selected = $state<Record<string, Entry>>({});
  let anchor = '';
  let trail = $state<Entry[]>([]);
  let details = $state<Entry | null>(null);
  let filtersOpen = $state(false);
  let gridSize = $state<'small' | 'medium' | 'large'>('medium');
  onMount(() => {
    try {
      const saved = localStorage.getItem('httpwrapper.grid-size');
      if (saved === 'small' || saved === 'medium' || saved === 'large') gridSize = saved;
    } catch {
      /* Appearance still works when browser storage is unavailable. */
    }
  });
  let refresh = $state(0);
  let listKey = $state(0);
  let loadController: AbortController | undefined;
  let generation = 0;
  let lastRefresh = 0;
  let name = $state(untrack(() => route.filters.name ?? ''));
  let pendingName: string | undefined;
  let nameTimer: ReturnType<typeof setTimeout>;
  let nameInput: HTMLInputElement;
  let thumbnails = $state<Record<string, string>>({});
  const thumbnailStates: Record<string, Schema['Thumbnail']> = {};
  let visible: Entry[] = [];
  let thumbnailBusy = false;
  let thumbnailController = new AbortController();
  let thumbnailTimer: ReturnType<typeof setTimeout>;
  let selectedEntries = $derived(Object.values(selected));
  let hidden = $derived(
    selectedEntries.filter((item) => !entries.some((entry) => entry.id === item.id)).length,
  );
  let title = $derived(
    trail.at(-1)?.name ?? (route.directory === 'root' ? profile.name : 'Folder'),
  );
  let pinned = $derived(
    bookmarks.some(
      (item) => item.profile_id === profile.id && item.directory_id === route.directory,
    ),
  );
  let folderKey = $derived(`${profile.id}:${route.directory}`);
  let queryKey = $derived(
    JSON.stringify({
      profile: profile.id,
      directory: route.directory,
      filters: route.filters,
      sort: route.view.sort,
      direction: route.view.direction,
      folders: route.view.folders_first,
    }),
  );

  $effect(() => {
    const current = route;
    untrack(() => {
      clearTimeout(nameTimer);
      pendingName = undefined;
      name = current.filters.name ?? '';
    });
  });

  $effect(() => {
    void folderKey;
    untrack(() => {
      selected = {};
      anchor = '';
      details = null;
      trail = [];
      clearTimeout(nameTimer);
    });
  });
  $effect(() => {
    void folderKey;
    void profileRevision;
    void profile.previews_enabled;
    untrack(() => {
      thumbnails = {};
      for (const id of Object.keys(thumbnailStates)) delete thumbnailStates[id];
      visible = [];
      thumbnailController.abort();
      thumbnailController = new AbortController();
    });
  });
  $effect(() => {
    void queryKey;
    void profileRevision;
    const version = refresh;
    untrack(() => {
      const pid = profile.id,
        directory = route.directory,
        filters = route.filters,
        sort = route.view.sort,
        direction = route.view.direction,
        folders = route.view.folders_first;
      const reload = version !== lastRefresh;
      lastRefresh = version;
      loadController?.abort();
      loadController = new AbortController();
      const signal = loadController.signal;
      const current = ++generation;
      entries = [];
      page = null;
      error = '';
      code = '';
      requestId = '';
      busy = true;
      listKey++;
      void load(false, reload, current, signal, filters, pid, directory, sort, direction, folders);
      void breadcrumbs(pid, directory, current, signal);
    });
  });
  onDestroy(() => {
    loadController?.abort();
    thumbnailController.abort();
    clearTimeout(nameTimer);
    clearTimeout(thumbnailTimer);
  });
  async function breadcrumbs(pid: string, directory: string, current: number, signal: AbortSignal) {
    const result: Entry[] = [];
    const seen: string[] = [];
    try {
      while (directory !== 'root' && result.length < 64 && !seen.includes(directory)) {
        seen.push(directory);
        const entry = await api<Entry>(`/profiles/${idPath(pid)}/entries/${idPath(directory)}`, {
          signal,
        });
        result.unshift(entry);
        directory = entry.parent_id;
      }
      if (current === generation) trail = result;
    } catch {
      /* Listing errors provide the recovery action. */
    }
  }
  async function load(
    more: boolean,
    reload = false,
    current = generation,
    signal = loadController?.signal,
    filters = route.filters,
    pid = profile.id,
    directory = route.directory,
    sort = route.view.sort,
    direction = route.view.direction,
    folders = route.view.folders_first,
  ) {
    if (more && (!page?.next_cursor || busy)) return;
    busy = true;
    error = '';
    try {
      const query = {
        directory_id: directory,
        ...filters,
        sort,
        direction,
        folders_first: folders,
        limit: 500,
      };
      let result = await api<Schema['BrowsePage']>(`/profiles/${idPath(pid)}/browse`, {
        ...(signal ? { signal } : {}),
        query: {
          ...query,
          ...(more ? { cursor: page?.next_cursor } : reload ? { refresh: true } : {}),
        },
      });
      const restored = [...result.entries];
      const restoreCount = !more && route.scroll > 0 ? route.loaded : 0;
      while (restored.length < restoreCount && result.next_cursor && current === generation) {
        result = await api<Schema['BrowsePage']>(`/profiles/${idPath(pid)}/browse`, {
          ...(signal ? { signal } : {}),
          query: { ...query, cursor: result.next_cursor },
        });
        restored.push(...result.entries);
      }
      result = { ...result, entries: restored };
      if (current !== generation) return;
      entries = more ? [...entries, ...result.entries] : result.entries;
      page = result;
    } catch (e) {
      if (current === generation) {
        error = errorMessage(e);
        if (e instanceof ApiError) {
          code = e.code;
          requestId = e.requestId;
        }
      }
    } finally {
      if (current === generation) busy = false;
    }
  }
  function navigate(directory: string) {
    onroute({ ...route, directory, filters: {}, scroll: 0 });
  }
  function consumeFilters(filters: Schema['PresetFilters'], preserveName = true) {
    const combined =
      preserveName && pendingName !== undefined ? { ...filters, name: pendingName } : filters;
    clearTimeout(nameTimer);
    pendingName = undefined;
    name = combined.name ?? '';
    return Object.fromEntries(
      Object.entries(combined).filter(([, value]) => value !== '' && value !== undefined),
    );
  }
  function changeFilters(filters: Schema['PresetFilters'], preserveName = true) {
    const active = consumeFilters(filters, preserveName);
    onroute({ ...route, filters: active, scroll: 0 });
  }
  function changeView(patch: Partial<View>) {
    const view = { ...route.view, ...patch };
    onroute({
      ...route,
      filters: consumeFilters(route.filters),
      view,
      scroll: 0,
    });
    void onsavepreferences({ profile_views: { [profile.id]: view } }).catch((e) =>
      onnotice(errorMessage(e)),
    );
  }
  function select(id: string, range: boolean) {
    selected = selectionRange(entries, selected, id, anchor, range);
    if (!range) anchor = id;
  }
  function open(entry: Entry) {
    if (entry.kind === 'directory') navigate(entry.id);
    else onopen(entry, entries);
  }
  function inspect(entry: Entry) {
    details = entry;
  }
  function onvisible(items: Entry[]) {
    visible = items;
    void requestThumbnails();
  }
  async function requestThumbnails() {
    if (thumbnailBusy || !profile.previews_enabled || route.view.view !== 'grid') return;
    const items = visible
      .filter(
        (entry) =>
          ['image', 'video'].includes(entry.category) &&
          (!thumbnailStates[entry.id] || thumbnailStates[entry.id]?.state === 'pending'),
      )
      .slice(0, 32);
    if (!items.length) return;
    const signal = thumbnailController.signal;
    thumbnailBusy = true;
    try {
      const result = await api<{ thumbnails: Schema['Thumbnail'][] }>(
        `/profiles/${idPath(profile.id)}/thumbnails`,
        {
          method: 'POST',
          body: { entry_ids: items.map((entry) => entry.id) },
          signal,
        },
      );
      if (signal.aborted) return;
      for (const thumbnail of result.thumbnails) {
        thumbnailStates[thumbnail.entry_id] = thumbnail;
        if (thumbnail.state === 'ready')
          thumbnails = {
            ...thumbnails,
            [thumbnail.entry_id]: endpoint(`/thumbnails/${idPath(thumbnail.id)}`),
          };
      }
    } catch {
      /* A type icon remains usable when thumbnails are unavailable. */
    } finally {
      thumbnailBusy = false;
      clearTimeout(thumbnailTimer);
      if (!signal.aborted) thumbnailTimer = setTimeout(() => void requestThumbnails(), 2000);
    }
  }
  function filterName() {
    clearTimeout(nameTimer);
    const intended = name;
    pendingName = intended;
    nameTimer = setTimeout(() => changeFilters({ ...route.filters, name: intended }, false), 250);
  }
  function moreFilters() {
    if (pendingName !== undefined) changeFilters(route.filters);
    filtersOpen = true;
  }
</script>

<svelte:window
  onkeydown={(event) => {
    if (
      event.key === '/' &&
      !event.ctrlKey &&
      !event.metaKey &&
      !event.altKey &&
      !(event.target as HTMLElement).closest('input,textarea,select,[contenteditable="true"]') &&
      !document.querySelector('dialog[open]')
    ) {
      event.preventDefault();
      nameInput.focus();
    }
  }}
/>

<div class="location-bar">
  <button class="icon-button sidebar-toggle" aria-label="Toggle sidebar" onclick={onmenu}
    ><Icon name="menu" /></button
  ><button
    class="icon-button parent-button"
    aria-label="Parent folder"
    disabled={!page?.parent_id}
    onclick={() => navigate(page!.parent_id!)}><Icon name="arrow-up" /></button
  >
  <nav class="breadcrumbs" aria-label="Folder path">
    <button class="source-chip" onclick={() => navigate('root')}
      ><Icon name="server" size={15} />{profile.name}</button
    >{#each trail as entry (entry.id)}<Icon name="chevron-right" size={12} /><button
        aria-current={entry.id === route.directory ? 'page' : undefined}
        onclick={() => navigate(entry.id)}>{entry.name}</button
      >{/each}
  </nav>
  <div class="location-actions">
    <button class="button secondary edit-profile-button" aria-label="Edit profile" onclick={() => onprofile('edit')}
      ><Icon name="edit" size={17} /><span class="edit-profile-desktop">Edit profile</span><span class="edit-profile-mobile">Edit</span></button
    >
  </div>
</div>
<div class="folder-heading">
  <div>
    <div class="title-line">
      <h1>{title}</h1>
      <button
        class="icon-button pin-button"
        class:pinned
        aria-label={pinned ? 'Unpin folder' : 'Pin folder'}
        aria-pressed={pinned}
        onclick={() => onpin(profile.id, route.directory, title)}
        ><Icon name="pin" size={18} /></button
      >
    </div>
    <p>
      {page
        ? `${page.total_entries.toLocaleString()} items`
        : 'HTTP folder'}{profile.credential_state === 'temporary' ? ' · Temporary password' : ''}
    </p>
  </div>
  <div class="folder-actions">
    <button
      class="button secondary scan-button"
      onclick={() => onscan(profile.id, route.directory)}
      ><Icon name="scan" size={16} />Find files</button
    ><button class="icon-button refresh-button" aria-label="Refresh folder" onclick={() => refresh++}
      ><Icon name="refresh" size={18} /></button
    >
  </div>
</div>
<div class="toolbar">
  <label class="filter-field"
    ><Icon name="search" size={17} /><input
      bind:this={nameInput}
      aria-label="Filter names"
      placeholder="Filter names…"
      bind:value={name}
      oninput={filterName}
      maxlength="512"
    /></label
  >
  <label class="select-control"
    ><span class="sr-only">File type</span><select
      value={route.filters.category ?? ''}
      onchange={(event) =>
        changeFilters({
          ...route.filters,
          category: event.currentTarget.value as Schema['Category'] | '',
        })}
      ><option value="">All types</option
      >{#each ['folder', 'video', 'image', 'audio', 'document', 'archive', 'other'] as category (category)}<option
          value={category}>{category.charAt(0).toUpperCase() + category.slice(1)}</option
        >{/each}</select
      ><Icon name="chevron-down" size={15} /></label
  >
  <button class="icon-button" aria-label="More filters" onclick={moreFilters}
    ><Icon name="filter" size={18} /></button
  >
  <div class="toolbar-end">
    <div class="sort-options">
      <label class="check-label"
        ><input
          type="checkbox"
          checked={route.view.folders_first}
          onchange={(event) => changeView({ folders_first: event.currentTarget.checked })}
        />Folders first</label
      >
      <label class="select-control sort-control"
        ><span class="sr-only">Sort by</span><Icon name="sort" size={16} /><select
          value={route.view.sort}
          onchange={(event) => changeView({ sort: event.currentTarget.value as View['sort'] })}
          ><option value="name">Name</option><option value="size">Size</option><option
            value="modified">Modified</option
          ><option value="type">Type</option></select
          ><Icon name="chevron-down" size={15} />
        </label
      ><button
        class="icon-button"
        aria-label={route.view.direction === 'asc' ? 'Ascending' : 'Descending'}
        onclick={() =>
          changeView({
            direction: route.view.direction === 'asc' ? 'desc' : 'asc',
          })}
        ><Icon
          name={route.view.direction === 'asc' ? 'arrow-up' : 'chevron-down'}
          size={16}
        /></button
      >
    </div>
    <div class="view-options">
      {#if route.view.view === 'grid'}<label class="select-control"
          ><span class="sr-only">Grid size</span><select
            value={gridSize}
            onchange={(event) => {
              gridSize = event.currentTarget.value as typeof gridSize;
              try {
                localStorage.setItem('httpwrapper.grid-size', gridSize);
              } catch {
                /* Keep the current choice for this view. */
              }
            }}
            ><option value="small">Small tiles</option><option value="medium">Medium tiles</option
            ><option value="large">Large tiles</option></select
            ><Icon name="chevron-down" size={15} />
          </label
        >{/if}

      <div class="view-switch" aria-label="View mode">
        {#each [['detailed', 'details', 'Detailed view'], ['compact', 'list', 'Compact view'], ['grid', 'grid', 'Grid view']] as mode (mode[0])}<button
            aria-label={mode[2]}
            aria-pressed={route.view.view === mode[0]}
            class:active={route.view.view === mode[0]}
            onclick={() => changeView({ view: mode[0] as View['view'] })}
            ><Icon name={mode[1]!} size={17} /></button
          >{/each}
      </div>
    </div>
  </div>
</div>
{#if Object.values(route.filters).some((value) => value !== '' && value !== undefined)}
  <div class="filter-summary">
    {#each Object.entries(route.filters).filter(([, value]) => value !== '' && value !== undefined) as [key, value] (key)}<button
        class="filter-chip"
        aria-label={`Remove ${key} filter`}
        onclick={() => {
          const next = { ...route.filters };
          delete next[key as keyof typeof next];
          changeFilters(next, key !== 'name');
        }}>{key.replaceAll('_', ' ')}: {String(value)} ×</button
      >{/each}{#if Object.keys(route.filters).length}<button
        class="text-button"
        onclick={() => changeFilters({}, false)}>Clear filters</button
      >{/if}
  </div>
{/if}
{#if page?.stale}<p class="warning-banner" role="status">
    Cached listing from {new Date(page.fetched_at).toLocaleString()}. The source could not be
    refreshed.
  </p>{/if}
{#if error}<div class="error-banner" role="alert">
    <p>{error}</p>
    {#if requestId}<small>Request: {requestId}</small>{/if}
    <div class="error-actions">
      <button class="button secondary" onclick={() => refresh++}>Retry</button
      >{#if code === 'credentials_required' || code === 'upstream_auth_required'}<button
          class="button primary"
          onclick={() => onprofile(profile.credential_state === 'remembered' ? 'edit' : 'unlock')}
          >{profile.credential_state === 'remembered'
            ? 'Edit credentials'
            : 'Unlock profile'}</button
        >{/if}{#if code === 'snapshot_expired'}<span
          >The folder changed. Refresh to load a new listing.</span
        >{/if}
    </div>
  </div>{/if}
{#if entries.length}
  {#key `${listKey}:${route.view.view}`}<FileList
      {entries}
      {selected}
      view={route.view}
      compact={preferences.density === 'compact'}
      {gridSize}
      total={page?.total_entries ?? entries.length}
      {busy}
      hasMore={!!page?.next_cursor}
      initialScroll={route.scroll}
      {thumbnails}
      onselect={select}
      onopen={open}
      ondetails={inspect}
      onmore={() => void load(true)}
      {onvisible}
      onscroll={(scroll) =>
        history.replaceState({ ...history.state, scroll, loaded: entries.length }, '')}
      onsort={(sort) =>
        changeView({
          sort,
          direction: route.view.sort === sort && route.view.direction === 'asc' ? 'desc' : 'asc',
        })}
      onselectall={() => {
        selected = Object.fromEntries(entries.map((entry) => [entry.id, entry]));
        onnotice(`${entries.length} loaded items selected.`);
      }}
      onclear={() => (selected = {})}
      onup={() => {
        if (page?.parent_id) navigate(page.parent_id);
      }}
    />{/key}
{:else if busy}<div class="empty-state" role="status">
    <Icon name="folder" size={38} />
    <h2>Loading folder…</h2>
    <p>You can switch profiles while this folder loads.</p>
  </div>
{:else if !error}<div class="empty-state">
    <Icon name="folder" size={38} />
    <h2>
      {Object.keys(route.filters).length
        ? 'No files match these filters.'
        : 'This folder is empty.'}
    </h2>
    {#if Object.keys(route.filters).length}<button
        class="button secondary"
        onclick={() => changeFilters({}, false)}>Clear filters</button
      >{/if}
  </div>{/if}
{#if selectedEntries.length}<div class="selection-bar">
    <div>
      <strong>{selectedEntries.length} selected{hidden ? ` (${hidden} hidden)` : ''}</strong><button
        class="icon-button"
        aria-label="Clear selection"
        onclick={() => (selected = {})}><Icon name="x" size={16} /></button
      >
    </div>
    <div>
      <button class="button secondary" onclick={() => ontransfer(selectedEntries, 'browser')}
        >Download</button
      ><button class="button primary" onclick={() => ontransfer(selectedEntries, 'server')}
        >Save to server</button
      >
    </div>
  </div>{/if}
<footer class="folder-footer">
  <span
    >{entries.length.toLocaleString()} loaded{page
      ? ` of ${page.total_entries.toLocaleString()}`
      : ''}</span
  ><span class="footer-hint"
    >{page ? `Updated ${new Date(page.fetched_at).toLocaleTimeString()}` : 'Read-only source'}</span
  >
</footer>
{#if filtersOpen}<Filters
    filters={route.filters}
    {preferences}
    profileId={profile.id}
    onapply={(filters) => changeFilters(filters, false)}
    onsave={onsavepreferences}
    onclose={() => (filtersOpen = false)}
  />{/if}
{#if details}<Dialog title={details.name} onclose={() => (details = null)}
    ><div class="detail-icon"><FileMark entry={details} large /></div>
    <dl class="metadata">
      <dt>Type</dt>
      <dd>{details.category}</dd>
      <dt>Size</dt>
      <dd>
        {details.size != null && !details.size_exact ? 'Approximately ' : ''}{formatBytes(
          details.size,
        )}
      </dd>
      <dt>Modified</dt>
      <dd>{formatDate(details)}</dd>
      <dt>Path</dt>
      <dd>{details.escaped_path}</dd>
    </dl>
    <div class="modal-actions">
      {#if details.kind === 'directory'}<button
          class="button secondary"
          onclick={() => {
            onpin(profile.id, details!.id, details!.name);
          }}>Pin folder</button
        ><button
          class="button primary"
          onclick={() => {
            const item = details!;
            details = null;
            navigate(item.id);
          }}>Open folder</button
        >{:else}<button
          class="button secondary"
          onclick={() => {
            const item = details!;
            details = null;
            ontransfer([item], 'server');
          }}>Save to server</button
        ><button
          class="button secondary"
          onclick={() => {
            const item = details!;
            details = null;
            ontransfer([item], 'browser');
          }}>Download</button
        ><button
          class="button primary"
          onclick={() => {
            const item = details!;
            details = null;
            onopen(item, entries);
          }}>Preview</button
        >{/if}
    </div></Dialog
  >{/if}
