<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import {
    api,
    ApiError,
    browserDownload,
    errorMessage,
    formatBytes,
    idPath,
    type Download,
    type Schema,
  } from '../api';
  import Dialog from '../ui/Dialog.svelte';
  import './activity.css';

  let { revision, onnotice }: { revision: number; onnotice: (message: string) => void } = $props();
  let jobs = $state<Download[]>([]);
  let destinations = $state<Schema['Destination'][]>([]);
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');
  let filter = $state('');
  let cursor = $state('');
  let previous = $state<string[]>([]);
  let next = $state<string | null>(null);
  let confirmation = $state<{
    job: Download;
    action: 'restart' | 'retry' | 'partial' | 'content';
  } | null>(null);
  let controller: AbortController | undefined;
  const groups = ['Active', 'Completed', 'Failed', 'Cancelled'];
  const group = (job: Download) =>
    job.state === 'completed'
      ? 'Completed'
      : job.state === 'failed'
        ? 'Failed'
        : job.state === 'cancelled'
          ? 'Cancelled'
          : 'Active';
  const stateLabel = (value: string) =>
    value === 'waiting_for_credentials'
      ? 'Password needed'
      : value.charAt(0).toUpperCase() + value.slice(1).replaceAll('_', ' ');

  $effect(() => {
    void revision;
    untrack(() => void load());
  });
  onDestroy(() => controller?.abort());
  async function load() {
    controller?.abort();
    const request = new AbortController();
    controller = request;
    loading = true;
    error = '';
    try {
      const [page, storage] = await Promise.all([
        api<{ downloads: Download[]; next_cursor: string | null }>('/downloads', {
          query: { limit: 100, cursor, state: filter },
          signal: request.signal,
        }),
        api<{ destinations: Schema['Destination'][] }>('/destinations', { signal: request.signal }),
      ]);
      if (request.signal.aborted) return;
      jobs = page.downloads;
      next = page.next_cursor;
      destinations = storage.destinations;
    } catch (e) {
      if (!request.signal.aborted) error = errorMessage(e);
    } finally {
      if (!request.signal.aborted) loading = false;
    }
  }
  function firstPage() {
    cursor = '';
    previous = [];
    void load();
  }
  function pageForward() {
    if (!next) return;
    previous = [...previous, cursor];
    cursor = next;
    void load();
  }
  function pageBack() {
    cursor = previous.at(-1) ?? '';
    previous = previous.slice(0, -1);
    void load();
  }
  async function action(job: Download, name: string, allowRestart = false) {
    busy = job.id;
    error = '';
    try {
      await api(`/downloads/${idPath(job.id)}/${name}`, {
        method: 'POST',
        ...(name === 'resume' ? { body: { allow_restart: allowRestart } } : {}),
      });
      confirmation = null;
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.code === 'restart_required')
        confirmation = { job, action: 'restart' };
      else error = errorMessage(e);
    } finally {
      busy = '';
    }
  }
  async function confirm() {
    if (!confirmation) return;
    const { job, action: name } = confirmation;
    if (name === 'restart' || name === 'retry') {
      await action(job, name === 'restart' ? 'resume' : 'retry', name === 'restart');
      return;
    }
    busy = job.id;
    error = '';
    try {
      await api(`/downloads/${idPath(job.id)}/${name}`, { method: 'DELETE' });
      confirmation = null;
      onnotice(
        name === 'content'
          ? 'Downloaded file deleted from the server.'
          : 'Partial file removed from the server.',
      );
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }
  async function download(job: Download) {
    try {
      await browserDownload(`/downloads/${idPath(job.id)}/content`);
      onnotice('Download requested. Check your browser downloads.');
    } catch (e) {
      error = errorMessage(e);
    }
  }
</script>

<section class="page-scroll activity-page" aria-label="Server downloads">
  <div class="page-heading">
    <div>
      <h1>Server queue</h1>
      <p>Transfers saved to a server location. Browser downloads appear in your browser.</p>
    </div>
    <button class="button secondary" onclick={() => void load()} disabled={loading}>Refresh</button>
  </div>
  <div class="activity-filters">
    <label class="form-field"
      >Job state<select bind:value={filter} onchange={firstPage}
        ><option value="">All states</option
        >{#each ['queued', 'running', 'paused', 'waiting_for_credentials', 'interrupted', 'completed', 'failed', 'cancelled'] as state (state)}<option
            value={state}>{stateLabel(state)}</option
          >{/each}</select
      ></label
    >
  </div>
  {#if error}<div class="error-banner" role="alert">
      {error}<button class="button secondary" onclick={() => void load()}>Retry loading</button>
    </div>{/if}
  {#if loading}<p class="activity-note" role="status">Updating server queue…</p>{/if}
  {#if !loading && !error && jobs.length === 0}<p class="activity-empty">
      No server transfers{filter ? ' in this state' : ''}. Choose a file and use Save to server.
    </p>{/if}
  {#each groups as section (section)}
    {@const items = jobs.filter((job) => group(job) === section)}
    {#if items.length}<h2 class="activity-section">{section}</h2>
      <div class="job-list">
        {#each items as job (job.id)}
          <article class="job" aria-label={job.saved_name ?? job.name}>
            <div class="job-main">
              <strong>{job.saved_name ?? job.name}</strong>
              <div class="job-meta">
                <span class="activity-state">{stateLabel(job.state)}</span><span
                  >{destinations.find((destination) => destination.id === job.destination_id)
                  ?.label ?? job.destination_id}{job.destination_path ? ` / ${job.destination_path}` : ''}</span
                ><span
                  >{formatBytes(job.transferred_bytes)} transferred{job.total_bytes !== null
                    ? ` of ${formatBytes(job.total_bytes)}`
                    : ' · Total unknown'}</span
                >
              </div>
              {#if job.state === 'running' || job.state === 'queued' || job.state === 'paused'}
                {#if job.total_bytes !== null && job.total_bytes > 0}<progress
                    aria-label={`Progress for ${job.name}`}
                    value={job.transferred_bytes}
                    max={job.total_bytes}
                  ></progress>{:else if job.state === 'running'}<progress
                    aria-label={`Downloading ${job.name}; total size unknown`}
                  ></progress>{/if}
              {/if}
              {#if job.state === 'running'}<div class="job-meta">
                  <span
                    >{job.bytes_per_second !== null
                      ? `${formatBytes(job.bytes_per_second)}/s`
                      : 'Speed not yet known'}</span
                  ><span
                    >{job.eta_seconds !== null
                      ? `About ${Math.max(1, Math.ceil(job.eta_seconds / 60))} min remaining`
                      : 'Time remaining unknown'}</span
                  >
                </div>{/if}
              {#if job.state === 'waiting_for_credentials'}<p class="activity-note">
                  Unlock this profile from the sidebar, then resume the transfer.
                </p>{/if}
              {#if job.fallback_reason}<p class="activity-note">
                  {job.connections} connection{job.connections === 1 ? '' : 's'} · {job.fallback_reason}
                </p>{/if}
              {#if job.error}<p class="activity-note">
                  {job.error.message} ({job.error.code})
                </p>{/if}
              {#if job.state === 'completed' && !job.file_available}<p class="activity-note">
                  The downloaded file is no longer available on this server.
                </p>{/if}
              {#if job.destination_id !== 'server' && !destinations.find((destination) => destination.id === job.destination_id)?.available}
                <p class="activity-note">This mounted location is unavailable. Restore the mount to access the file.</p>
              {/if}
            </div>
            <div class="job-actions">
              {#if ['running', 'queued'].includes(job.state)}<button
                  class="button secondary"
                  disabled={busy === job.id}
                  onclick={() => action(job, 'pause')}>Pause</button
                >{/if}
              {#if ['paused', 'interrupted', 'waiting_for_credentials'].includes(job.state)}<button
                  class="button secondary"
                  disabled={busy === job.id}
                  onclick={() => action(job, 'resume')}>Resume</button
                >{/if}
              {#if !['completed', 'cancelled', 'failed'].includes(job.state)}<button
                  class="button secondary"
                  disabled={busy === job.id}
                  onclick={() => action(job, 'cancel')}>Cancel transfer</button
                >{/if}
              {#if ['failed', 'cancelled'].includes(job.state)}<button
                  class="button secondary"
                  disabled={busy === job.id || !job.profile_id}
                  onclick={() => (confirmation = { job, action: 'retry' })}>Retry</button
                ><button
                  class="button secondary"
                  disabled={busy === job.id}
                  onclick={() => (confirmation = { job, action: 'partial' })}
                  >Remove partial file</button
                >{/if}
              {#if job.state === 'completed' && job.file_available}<button
                  class="button primary"
                  disabled={job.destination_id !== 'server' && !destinations.find((destination) => destination.id === job.destination_id)?.available}
                  onclick={() => download(job)}>Download</button
                >{#if job.destination_id === 'server'}<button
                    class="button secondary"
                    disabled={busy === job.id}
                    onclick={() => (confirmation = { job, action: 'content' })}
                    >Delete downloaded file</button
                  >{/if}{/if}
            </div>
          </article>
        {/each}
      </div>{/if}
  {/each}
  <nav class="activity-pagination" aria-label="Download pages">
    <button class="button secondary" disabled={loading || !previous.length} onclick={pageBack}
      >Previous page</button
    ><span>Page {previous.length + 1} · Up to 100 jobs</span><button
      class="button secondary"
      disabled={loading || !next}
      onclick={pageForward}>Next page</button
    >
  </nav>
  <p class="activity-note">
    Cancelling keeps partial data until you remove it. These actions affect files on this server
    only.
  </p>
</section>
{#if confirmation}
  <Dialog
    title={confirmation.action === 'content'
      ? 'Delete downloaded file?'
      : confirmation.action === 'partial'
        ? 'Remove partial file?'
        : confirmation.action === 'retry' && confirmation.job.destination_id !== 'server'
          ? 'Retry transfer?'
        : 'Restart from the beginning?'}
    onclose={() => {
      if (!busy) confirmation = null;
    }}
  >
    <p class="activity-confirm-copy">
      {#if confirmation.action === 'content'}Delete <strong>{confirmation.job.name}</strong> from this
        server? The upstream file is unchanged.{:else if confirmation.action === 'partial'}Remove
        the partial local data for <strong>{confirmation.job.name}</strong>? The transfer history
        and upstream file are kept.{:else if confirmation.action === 'retry' && confirmation.job.destination_id !== 'server'}Try saving <strong>{confirmation.job.name}</strong> again. If a verified local copy is available, it will be reused. Otherwise, the file will download again.{:else}<strong>{confirmation.job.name}</strong> will restart from zero.
        Its partial local data will be discarded.{/if}
    </p>
    {#if error}<p class="error-banner" role="alert">{error}</p>{/if}
    <div class="modal-actions">
      <button class="button secondary" disabled={!!busy} onclick={() => (confirmation = null)}
        >Keep as is</button
      ><button class="button primary" disabled={!!busy} onclick={confirm}
        >{confirmation.action === 'content'
          ? 'Delete downloaded file'
          : confirmation.action === 'partial'
            ? 'Remove partial file'
            : confirmation.action === 'retry' && confirmation.job.destination_id !== 'server'
              ? 'Retry transfer'
            : 'Restart download'}</button
      >
    </div>
  </Dialog>
{/if}
