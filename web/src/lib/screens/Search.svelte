<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { api, ApiError, browserDownload, contentPath, errorMessage, formatBytes, formatDate, idPath, type Entry, type Profile, type Scan, type Schema } from '../api';
  import { fileTypes, type FileType, type FindRun } from '../find';
  import './activity.css';

  let { profiles, find, sourcePath, scan, revision, onopen, onbrowse, onback, onscan, onsave, onnotice, onrefresh, onfilters }: {
    profiles: Profile[];
    find: FindRun | null;
    sourcePath: string;
    scan: Scan | null;
    revision: number;
    onopen: (entry: Entry) => void;
    onbrowse: (profileId: string, directoryId: string) => void;
    onback: () => void;
    onscan: (profileId: string, directoryId: string) => void;
    onsave: (entries: Entry[]) => void;
    onnotice: (message: string) => void;
    onrefresh: () => void;
    onfilters: (types: FileType[], query: string) => void;
  } = $props();
  let query = $state('');
  let appliedQuery = $state('');
  let types = $state<FileType[]>([]);
  let entries = $state<Entry[]>([]);
  let coverage = $state<Schema['Coverage'][]>([]);
  let selected = $state<string[]>([]);
  let error = $state('');
  let loading = $state(false);
  let cursor = $state('');
  let previous = $state<string[]>([]);
  let next = $state<string | null>(null);
  let activeId = '';
  let controller: AbortController | undefined;
  let selectedEntries = $derived(entries.filter((entry) => selected.includes(entry.id)));
  onDestroy(() => controller?.abort());

  $effect(() => {
    const id = find?.id ?? '';
    if (id === activeId) return;
    activeId = id;
    untrack(() => {
      controller?.abort();
      types = [...(find?.types ?? [])];
      query = find?.query ?? '';
      appliedQuery = query.trim();
      cursor = '';
      previous = [];
      entries = [];
      selected = [];
      coverage = [];
      next = null;
      error = '';
      if (id) void load();
    });
  });
  $effect(() => {
    void revision;
    if (find?.id && activeId === find.id) untrack(() => { void load(); });
  });

  async function load() {
    if (!find) return;
    controller?.abort();
    const request = new AbortController();
    controller = request;
    loading = true;
    error = '';
    try {
      const page = await api<Schema['SearchPage']>('/search', {
        query: {
          q: appliedQuery, profile_id: find.profileId, directory_id: find.directoryId,
          direct_only: find.directOnly || undefined,
          categories: (types.length ? types : fileTypes).join(','), cursor, limit: 100,
        }, signal: request.signal,
      });
      if (request.signal.aborted) return;
      entries = page.entries;
      coverage = page.coverage;
      next = page.next_cursor;
      const visible = new Set(page.entries.map((entry) => entry.id));
      selected = selected.filter((id) => visible.has(id));
    } catch (e) {
      if (request.signal.aborted) return;
      if (cursor && e instanceof ApiError && e.code === 'snapshot_expired') {
        cursor = '';
        previous = [];
        selected = [];
        void load();
        return;
      }
      error = errorMessage(e);
    } finally { if (!request.signal.aborted) loading = false; }
  }
  function search() { cursor = ''; previous = []; selected = []; appliedQuery = query.trim(); onfilters([...types], query); void load(); }
  function forward() { if (!next) return; previous = [...previous, cursor]; cursor = next; selected = []; void load(); }
  function back() { cursor = previous.at(-1) ?? ''; previous = previous.slice(0, -1); selected = []; void load(); }
  async function cancel() {
    if (!find) return;
    try { await api(`/scans/${idPath(find.id)}/cancel`, { method: 'POST' }); onrefresh(); }
    catch (e) {
      if (e instanceof ApiError && e.code === 'invalid_state') onrefresh();
      else error = errorMessage(e);
    }
  }
  async function download(entry: Entry) {
    try { await browserDownload(contentPath(entry)); onnotice('Download requested. Check your browser downloads.'); }
    catch (e) { error = errorMessage(e); }
  }
  const profileName = (id: string) => profiles.find((item) => item.id === id)?.name ?? 'Removed profile';
  const parentPath = (entry: Entry) => entry.escaped_path.slice(0, entry.escaped_path.lastIndexOf('/') + 1) || '/';
</script>

<section class="page-scroll activity-page" aria-label="Search results">
  <div class="page-heading"><div>
    <h1>Search results</h1>
    <p>{find ? `${sourcePath} · ${find.directOnly ? 'This folder only' : 'This folder and subfolders'}` : 'Open a folder to find files.'}</p>
  </div>{#if find}<button class="button secondary" onclick={onback}>Back to folder</button>{/if}</div>
  {#if !find}
    <p class="activity-empty">Choose Find files from a folder to get started.</p>
  {:else}
    {#if scan}
      <div class="activity-coverage" role="status">
        <strong>{scan.state === 'running' || scan.state === 'queued' ? 'Finding files…' : scan.state === 'completed' ? 'Finished finding files' : scan.state === 'cancelled' ? 'Stopped finding files' : 'File search stopped'}</strong>
        <p>{scan.directories_visited.toLocaleString()} folders checked · {scan.files_found.toLocaleString()} files found</p>
        {#if scan.error}<p>{scan.error.message}</p>{/if}
        {#if scan.state === 'running' || scan.state === 'queued'}<p>Results update as files are found.</p>{:else if scan.state !== 'completed' || scan.coverage.state !== 'complete'}<p>Results may be incomplete. Files already found remain available.</p>{/if}
        {#if ['running', 'queued'].includes(scan.state)}
          <button class="button secondary" onclick={cancel}>Cancel</button>
        {:else if ['failed', 'partial', 'cancelled', 'paused', 'interrupted', 'waiting_for_credentials'].includes(scan.state)}
          <button class="button secondary" onclick={() => onscan(find!.profileId, find!.directoryId)}>Find again</button>
        {/if}
      </div>
    {/if}
    <form onsubmit={(event) => { event.preventDefault(); search(); }}>
      <div class="activity-filters">
        <label class="form-field search-query">Filter results by name<input type="search" maxlength="512" bind:value={query} placeholder="Filename" /></label>
        <button class="button primary" type="submit" disabled={loading}>Show results</button>
      </div>
    </form>
    <fieldset class="find-choices">
      <legend>File types</legend>
      <label><input type="checkbox" checked={!types.length} onchange={() => { types = []; search(); }} />All files</label>
      {#each fileTypes as type (type)}<label><input type="checkbox" checked={types.includes(type)} onchange={(event) => { types = event.currentTarget.checked ? [...types, type] : types.filter((item) => item !== type); search(); }} />{type.charAt(0).toUpperCase() + type.slice(1)}</label>{/each}
    </fieldset>
    {#if error}<div class="error-banner" role="alert">{error}<button class="button secondary" onclick={search}>Try again</button></div>{/if}
    {#if loading}<p class="activity-note" role="status">Loading results…</p>{/if}
    {#if !loading && !error && !entries.length}<p class="activity-empty">No files match. Try another type or name.</p>{/if}
    {#if entries.length}
      <div class="activity-actions">
        <label class="result-checkbox"><input type="checkbox" aria-label="Select files on this page" checked={selected.length === entries.length} onchange={(event) => (selected = event.currentTarget.checked ? entries.map((entry) => entry.id) : [])} />Select files on this page</label>
        <span>{selectedEntries.length} selected</span>
        <button class="button secondary" disabled={!selectedEntries.length} onclick={() => onsave(selectedEntries)}>Save to server</button>
        <button class="button secondary" disabled={!selectedEntries.length} onclick={() => (selected = [])}>Clear selection</button>
      </div>
      <div class="job-list">
        {#each entries as entry (`${entry.profile_id}:${entry.id}`)}
          <article class="job" aria-label={entry.name}>
            <label class="result-checkbox"><input type="checkbox" aria-label={`Select ${entry.name}`} checked={selected.includes(entry.id)} onchange={(event) => (selected = event.currentTarget.checked ? [...selected, entry.id] : selected.filter((id) => id !== entry.id))} /></label>
            <div class="job-main">
              <button class="result-name" onclick={() => onopen(entry)}>{entry.name}</button>
              <span class="result-path">{profileName(entry.profile_id)} · {parentPath(entry)}</span>
              <div class="job-meta"><span>{entry.size !== null && !entry.size_exact ? 'About ' : ''}{formatBytes(entry.size)}</span><span>{formatDate(entry)}</span></div>
            </div>
            <div class="job-actions">
              <button class="button secondary" onclick={() => onbrowse(entry.profile_id, entry.parent_id)}>Open parent folder</button>
              <button class="button secondary" onclick={() => download(entry)}>Download</button>
              <button class="button secondary" onclick={() => onsave([entry])}>Save to server</button>
            </div>
          </article>
        {/each}
      </div>
    {/if}
    <nav class="activity-pagination" aria-label="Result pages">
      <button class="button secondary" disabled={!previous.length} onclick={back}>Previous page</button>
      <span>Page {previous.length + 1} · {entries.length} results</span>
      <button class="button secondary" disabled={!next} onclick={forward}>Next page</button>
    </nav>
    {#if coverage.some((item) => item.state === 'stale')}<p class="activity-note">The source may have changed since these files were found.</p>{/if}
  {/if}
</section>
