<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import {
    api,
    browserDownload,
    contentPath,
    currentSession,
    errorMessage,
    formatBytes,
    idPath,
    type Entry,
    type Preferences,
    type Schema,
  } from '../api';
  import Dialog from '../ui/Dialog.svelte';
  import { fileTypes, type FileType } from '../find';
  import type { Scan } from '../api';
  let {
    entries,
    mode,
    profileId = '',
    directoryId = 'root',
    defaultConnections = 1,
    initialDirectOnly = false,
    initialTypes = [],
    preferences,
    onsavepreferences,
    onclose,
    ondone,
    onscanstarted = () => {},
  }: {
    entries: Entry[];
    mode: 'server' | 'browser' | 'scan';
    profileId?: string;
    directoryId?: string;
    defaultConnections?: 1 | 2 | 4;
    initialDirectOnly?: boolean;
    initialTypes?: FileType[];
    preferences: Preferences;
    onsavepreferences: (patch: Partial<Preferences>) => Promise<void>;
    onclose: () => void;
    ondone: (message: string) => void;
    onscanstarted?: (scan: Scan, directOnly: boolean, types: FileType[]) => void;
  } = $props();
  let destinations = $state<Schema['Destination'][]>([]);
  let destinationsReady = $state(false);
  let destination = $state('');
  let folder = $state('');
  let folderInput = $state('');
  let folders = $state<{ name: string; path: string }[]>([]);
  let folderLoading = $state(false);
  let folderError = $state('');
  let folderRequest = 0;
  let favoriteBusy = $state(false);
  let favoriteError = $state('');
  let currentDestination = $derived(destinations.find((item) => item.id === destination));
  let favorites = $derived(preferences.destination_favorites ?? []);
  let isFavorite = $derived(favorites.some((item) => item.destination_id === destination && item.path === folder));
  let connections = $state<1 | 2 | 4>(untrack(() => defaultConnections));
  let bandwidth = $state<number | undefined>();
  let depth = $state(10);
  let maxEntries = $state(10000);
  let seconds = $state(300);
  let exclusions = $state('');
  let directOnly = $state(untrack(() => initialDirectOnly));
  let types = $state<FileType[]>(untrack(() => [...initialTypes]));
  let busy = $state(false);
  let error = $state('');
  let doneIds = $state<string[]>([]);
  let submitted = $state(false);
  let alive = true;
  const session = currentSession();
  let settings: {
    destination_id: string;
    destination_path: string;
    connections: 1 | 2 | 4;
    bytes_per_second: number | null;
  };
  let scanBody: {
    directory_id: string;
    max_depth: number;
    max_entries: number;
    max_seconds: number;
    excluded_directories: string[];
    categories: Schema['Category'][];
  };
  onDestroy(() => {
    alive = false;
  });
  const keys: Record<string, string> = {};
  function key(id: string) {
    return (keys[id] ??= crypto.randomUUID());
  }
  async function openFolder(id: string, path: string) {
    const request = ++folderRequest;
    destination = id;
    folderInput = path;
    folderError = '';
    folders = [];
    if (id === 'server') {
      folder = '';
      folderInput = '';
      folderLoading = false;
      return;
    }
    folderLoading = true;
    try {
      const result = await api<Schema['DestinationFolders']>(`/destinations/${idPath(id)}/folders`, { query: { path } });
      if (!alive || request !== folderRequest) return;
      folder = result.path;
      folderInput = result.path;
      folders = result.folders;
    } catch (e) {
      if (!alive || request !== folderRequest) return;
      folderError = errorMessage(e);
    } finally {
      if (request === folderRequest) folderLoading = false;
    }
  }
  async function toggleFavorite() {
    if (favoriteBusy || !currentDestination?.available || folderInput !== folder) return;
    favoriteBusy = true;
    favoriteError = '';
    const next = isFavorite
      ? favorites.filter((item) => item.destination_id !== destination || item.path !== folder)
      : [...favorites, { destination_id: destination, path: folder }];
    try { await onsavepreferences({ destination_favorites: next }); }
    catch (e) { favoriteError = errorMessage(e); }
    finally { favoriteBusy = false; }
  }
  async function removeFavorite(item: { destination_id: string; path: string }) {
    if (favoriteBusy) return;
    favoriteBusy = true;
    favoriteError = '';
    try { await onsavepreferences({ destination_favorites: favorites.filter((saved) => saved.destination_id !== item.destination_id || saved.path !== item.path) }); }
    catch (e) { favoriteError = errorMessage(e); }
    finally { favoriteBusy = false; }
  }
  function favoriteLabel(item: { destination_id: string; path: string }) {
    return `${destinations.find((entry) => entry.id === item.destination_id)?.label ?? item.destination_id} / ${item.path || 'Root'}`;
  }
  onMount(() => {
    const controller = new AbortController();
    if (mode === 'server')
      void api<{ destinations: Schema['Destination'][] }>('/destinations', {
        signal: controller.signal,
      })
        .then((result) => {
          destinations = result.destinations;
          destinationsReady = true;
          const last = preferences.last_destination;
          const selected = last?.destination_id ?? destinations[0]?.id ?? '';
          destination = selected;
          if (destinations.some((item) => item.id === selected && item.available))
            void openFolder(selected, last?.path ?? '');
          else if (selected) folderError = 'This saved location is unavailable. Choose another destination.';
        })
        .catch((e) => { error = errorMessage(e); destinationsReady = true; });
    return () => controller.abort();
  });
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy || currentSession() !== session) return;
    busy = true;
    error = '';
    if (!submitted) {
      settings = {
        destination_id: destination,
        destination_path: folder,
        connections,
        bytes_per_second: bandwidth ? bandwidth * 1024 : null,
      };
      scanBody = {
        directory_id: directoryId,
        max_depth: directOnly ? 0 : depth,
        max_entries: maxEntries,
        max_seconds: seconds,
        excluded_directories: exclusions
          .split('\n')
          .map((s) => s.trim())
          .filter(Boolean),
        categories: [],
      };
      submitted = true;
    }
    try {
      if (mode === 'scan') {
        const scan = await api<Scan>(`/profiles/${idPath(profileId)}/scans`, {
          method: 'POST',
          key: key('scan'),
          body: scanBody,
        });
        onscanstarted(scan, directOnly, [...types]);
        if (alive) onclose();
      } else {
        for (const entry of entries.filter(
          (entry) => entry.kind === 'file' && !doneIds.includes(entry.id),
        )) {
          if (currentSession() !== session) return;
          if (mode === 'browser') await browserDownload(contentPath(entry));
          else
            await api('/downloads', {
              method: 'POST',
              key: key(`${entry.profile_id}:${entry.id}`),
              body: {
                profile_id: entry.profile_id,
                entry_id: entry.id,
                ...settings,
              },
            });
          doneIds = [...doneIds, entry.id];
        }
        let rememberError = '';
        if (mode === 'server') {
          try { await onsavepreferences({ last_destination: { destination_id: destination, path: folder } }); }
          catch { rememberError = ' The destination could not be remembered.'; }
        }
        ondone(mode === 'browser'
          ? 'Download requested. Check your browser downloads.'
          : `${doneIds.length} file${doneIds.length === 1 ? '' : 's'} queued on the server.${rememberError}`);
        if (alive) onclose();
      }
    } catch (e) {
      if (currentSession() !== session) return;
      error = errorMessage(e);
      if (!alive)
        ondone(
          `${error} ${doneIds.length} confirmed accepted. Check ${mode === 'scan' ? 'file results' : mode === 'server' ? 'Server queue' : 'your browser downloads'} before trying again; a request may have been accepted before its response was lost.`,
        );
    } finally {
      busy = false;
    }
  }
</script>

<Dialog
  title={mode === 'scan'
    ? 'Find files'
    : mode === 'browser'
      ? 'Download to this device'
      : 'Save to server'}
  onclose={() => {
    if (!busy) onclose();
  }}
>
  <form onsubmit={submit}>
    <p class="modal-intro">
      {mode === 'scan'
        ? 'Choose where to look and which files to show. This does not download files.'
        : mode === 'server'
          ? 'Files are saved on the server running Folderhug, not this device.'
          : 'Your browser saves these files on this device. It may ask you to allow multiple downloads.'}
    </p>
    {#if error}<p class="error-banner" role="alert">
        {error}
        {doneIds.length ? `${doneIds.length} already accepted. Retry will skip those files.` : ''}
        {mode === 'browser'
          ? 'Retry will request only the remaining browser downloads.'
          : 'Retry uses the same request settings to avoid duplicate jobs. Check the activity list before starting a new request.'}
      </p>{/if}
    {#if mode === 'scan'}
      <fieldset class="find-choices" disabled={submitted}>
        <legend>Where to look</legend>
        <label><input type="radio" name="find-scope" checked={!directOnly} onchange={() => (directOnly = false)} />This folder and subfolders</label>
        <label><input type="radio" name="find-scope" checked={directOnly} onchange={() => (directOnly = true)} />This folder only</label>
      </fieldset>
      <fieldset class="find-choices" disabled={submitted}>
        <legend>Files to show</legend>
        <label><input type="checkbox" checked={!types.length} onchange={() => (types = [])} />All files</label>
        {#each fileTypes as type (type)}<label><input type="checkbox" checked={types.includes(type)} onchange={(event) => (types = event.currentTarget.checked ? [...types, type] : types.filter((item) => item !== type))} />{type.charAt(0).toUpperCase() + type.slice(1)}</label>{/each}
      </fieldset>
      <details>
        <summary>Scan limits and exclusions</summary>
        <div class="form-grid">
          <label class="form-field"
            >Folder depth<input
              type="number"
              min="0"
              max="64"
              required
              bind:value={depth}
              disabled={submitted}
            /></label
          >
          <label class="form-field"
            >Entry limit<input
              type="number"
              min="1"
              max="100000"
              required
              bind:value={maxEntries}
              disabled={submitted}
            /></label
          >
          <label class="form-field"
            >Time limit (seconds)<input
              type="number"
              min="1"
              max="1800"
              required
              bind:value={seconds}
              disabled={submitted}
            /></label
          >
        </div>
        <label class="form-field"
          >Excluded relative folders, one per line<textarea
            bind:value={exclusions}
            disabled={submitted}
            rows="3"
            placeholder="cache/"></textarea></label
        >
      </details>
      <p class="field-help">
        Up to {directOnly ? 0 : depth} folder levels, {maxEntries.toLocaleString()} entries, and {seconds}
        seconds. Results remain available if a limit is reached.
      </p>
    {:else}
      <ul class="transfer-files">
        {#each entries.slice(0, 20) as entry (entry.id)}<li>
            {entry.name}<small
              >{entry.size != null && !entry.size_exact ? 'About ' : ''}{formatBytes(
                entry.size,
              )}</small
            >
          </li>{/each}
      </ul>
      {#if entries.length > 20}<p class="field-help">
          And {entries.length - 20} more selected files.
        </p>{/if}
      {#if entries.some((entry) => entry.kind === 'directory')}<p class="error-banner">
          Folders need a scan and a review of the indexed files first. Close this dialog and choose
          Find files.
        </p>{/if}
      {#if mode === 'server'}
        <label class="form-field"
          >Destination<select bind:value={destination} onchange={(event) => void openFolder(event.currentTarget.value, '')} required disabled={submitted}
            >{#if destination && !currentDestination}<option value={destination}>Saved location unavailable</option>{/if}
              {#each destinations as item (item.id)}<option value={item.id}
                >{item.label}{item.available_bytes != null
                  ? ` (${formatBytes(item.available_bytes)} available)`
                  : ''}{item.available ? '' : ' (unavailable)'}</option
              >{/each}</select
          ></label
        >
        {#if currentDestination?.id === 'server'}<p class="field-help">This is Folderhug's built-in downloads folder.</p>{/if}
        {#if destinationsReady && destinations.length === 1 && destinations[0]?.id === 'server'}<p class="field-help">To save into a host folder, mount it directly under /mnt in Compose and restart Folderhug.</p>{/if}
        {#if currentDestination?.browseable}
          <div class="destination-picker">
            <label class="form-field">Folder inside {currentDestination.label}
              <input type="text" bind:value={folderInput} list="destination-folders" placeholder="Root folder" disabled={submitted} />
            </label>
            <datalist id="destination-folders">
              {#each folders as item (item.path)}<option value={item.path}></option>{/each}
            </datalist>
            <div class="destination-picker-actions">
              <button class="button secondary" type="button" disabled={submitted || folderLoading || !currentDestination.available} onclick={() => void openFolder(destination, folderInput)}>Open path</button>
              <button class="button secondary" type="button" disabled={submitted || folderLoading || !folder} onclick={() => void openFolder(destination, folder.split('/').slice(0, -1).join('/'))}>Up one folder</button>
              <button class="button secondary" type="button" disabled={submitted || favoriteBusy || !currentDestination.available || folderInput !== folder} onclick={() => void toggleFavorite()}>{isFavorite ? 'Remove favorite' : 'Add favorite'}</button>
            </div>
            {#if folderLoading}<p class="field-help" role="status">Opening folder…</p>{/if}
            {#if folderError}<p class="error-banner" role="alert">{folderError}</p>{/if}
            <p class="field-help">Selected folder: /{folder || ''}</p>
            {#if folders.length}<div class="destination-folder-list" aria-label="Subfolders">
              {#each folders as item (item.path)}<button class="destination-folder" type="button" disabled={submitted} onclick={() => void openFolder(destination, item.path)}>{item.name}/</button>{/each}
            </div>{/if}
          </div>
        {:else if folderError}<p class="error-banner" role="alert">{folderError}</p>{/if}
        {#if favorites.length}<div class="destination-favorites"><strong>Favorite locations</strong>
          {#each favorites as item (`${item.destination_id}:${item.path}`)}<div class="destination-favorite">
            <button class="button secondary" type="button" disabled={submitted} onclick={() => void openFolder(item.destination_id, item.path)}>{favoriteLabel(item)}</button>
            <button class="button secondary" type="button" aria-label={`Remove favorite ${favoriteLabel(item)}`} disabled={submitted || favoriteBusy} onclick={() => void removeFavorite(item)}>Remove</button>
          </div>{/each}
        </div>{/if}
        {#if favoriteError}<p class="error-banner" role="alert">{favoriteError}</p>{/if}
        <details>
          <summary>Transfer settings</summary><label class="form-field"
            >Connections per file<select bind:value={connections} disabled={submitted}
              ><option value={1}>1 connection</option><option value={2}>2 connections</option
              ><option value={4}>4 connections</option></select
            ></label
          >
          <p class="field-help">
            Multiple connections are used only when the source supports verified byte ranges.
          </p>
          <label class="form-field"
            >Speed limit (KiB/s, blank for unlimited)<input
              type="number"
              min="1"
              bind:value={bandwidth}
              disabled={submitted}
            /></label
          >
        </details>
      {/if}
    {/if}
    <div class="modal-actions">
      <button class="button secondary" type="button" disabled={busy} onclick={onclose}
        >Cancel</button
      ><button
        class="button primary"
        disabled={busy ||
          (mode === 'server' && (!currentDestination?.available || folderLoading || folderInput !== folder || !!folderError)) ||
          (mode !== 'scan' && entries.some((entry) => entry.kind === 'directory'))}
        >{busy
          ? 'Working…'
          : mode === 'scan'
            ? 'Find files'
            : mode === 'server'
              ? 'Queue files'
              : 'Download'}</button
      >
    </div>
  </form>
</Dialog>
