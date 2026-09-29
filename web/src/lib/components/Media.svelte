<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import {
    api,
    browserDownload,
    contentPath,
    endpoint,
    errorMessage,
    formatBytes,
    formatDate,
    idPath,
    type Entry,
  } from '../api';
  import Dialog from '../ui/Dialog.svelte';
  import FileMark from '../ui/FileMark.svelte';
  let {
    entry,
    previews,
    siblings,
    onclose,
    onchange,
    onsave,
    onnotice,
  }: {
    entry: Entry;
    previews: boolean;
    siblings: Entry[];
    onclose: () => void;
    onchange: (entry: Entry) => void;
    onsave: (entries: Entry[]) => void;
    onnotice: (message: string) => void;
  } = $props();
  let metadata = $state<Entry>(untrack(() => entry));
  let unavailable = $state(false);
  let loaded = $state(false);
  let zoom = $state(false);
  let error = $state('');
  const media = $derived(['image', 'video', 'audio'].includes(entry.category) && previews);
  const url = $derived(endpoint(contentPath(entry), { disposition: 'preview' }));
  onMount(() => {
    const controller = new AbortController();
    void api<Entry>(`/profiles/${idPath(entry.profile_id)}/entries/${idPath(entry.id)}`, {
      signal: controller.signal,
    })
      .then((value) => (metadata = value))
      .catch(() => {});
    const timeout = setTimeout(() => {
      if (!loaded) unavailable = true;
    }, 12000);
    return () => {
      controller.abort();
      clearTimeout(timeout);
    };
  });
  async function download() {
    try {
      await browserDownload(contentPath(entry));
      onnotice('Download requested. Check your browser downloads.');
    } catch (e) {
      error = errorMessage(e);
    }
  }
  function adjacent(delta: number) {
    const images = siblings.filter((item) => item.category === 'image');
    const i = images.findIndex((item) => item.id === entry.id);
    const next = images[i + delta];
    if (next) onchange(next);
  }
  let images = $derived(siblings.filter((item) => item.category === 'image'));
  let index = $derived(images.findIndex((item) => item.id === entry.id));
</script>

<Dialog title={entry.name} {onclose} wide>
  {#if error}<p role="alert" class="error-banner">{error}</p>{/if}
  {#if media && !unavailable}
    <div class="media-preview" class:zoomed={zoom}>
      {#if entry.category === 'image'}<img
          src={url}
          alt={entry.name}
          onload={() => (loaded = true)}
          onerror={() => (unavailable = true)}
        />
      {:else if entry.category === 'video'}<video
          src={url}
          controls
          preload="metadata"
          onloadedmetadata={() => (loaded = true)}
          onerror={() => (unavailable = true)}><track kind="captions" /></video
        >
      {:else}<audio
          src={url}
          controls
          preload="metadata"
          onloadedmetadata={() => (loaded = true)}
          onerror={() => (unavailable = true)}
        ></audio>{/if}
    </div>
    {#if entry.category === 'image'}<div class="media-controls">
        <button class="button secondary" disabled={index <= 0} onclick={() => adjacent(-1)}
          >Previous image</button
        ><button class="button secondary" onclick={() => (zoom = !zoom)}
          >{zoom ? 'Fit to window' : 'Zoom in'}</button
        ><button
          class="button secondary"
          disabled={index < 0 || index >= images.length - 1}
          onclick={() => adjacent(1)}>Next image</button
        >
      </div>{/if}
  {:else}<div class="file-preview-placeholder">
      <FileMark {entry} large />
      <p>
        {!previews
          ? 'Previews are disabled for this profile.'
          : 'Preview unavailable. You can download the file to open it.'}
      </p>
    </div>{/if}
  <dl class="metadata">
    <dt>Size</dt>
    <dd>
      {metadata.size != null && !metadata.size_exact ? 'Approximately ' : ''}{formatBytes(
        metadata.size,
      )}
    </dd>
    <dt>Modified</dt>
    <dd>
      {formatDate(metadata)}{metadata.modified_text && !metadata.timezone_known
        ? ' (source time, time zone unknown)'
        : ''}
    </dd>
    <dt>Path</dt>
    <dd>{entry.escaped_path}</dd>
  </dl>
  <div class="modal-actions">
    <button
      class="button secondary"
      onclick={async () => {
        try {
          await navigator.clipboard.writeText(entry.escaped_path);
          onnotice('Path copied.');
        } catch {
          error = 'Could not copy the path.';
        }
      }}>Copy path</button
    ><button class="button secondary" onclick={() => onsave([entry])}>Save to server</button><button
      class="button primary"
      onclick={download}>Download</button
    >
  </div>
</Dialog>
