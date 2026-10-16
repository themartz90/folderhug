<script lang="ts">
  import type { Schema } from '../api';
  import { releaseURL } from '../releases';
  import Dialog from '../ui/Dialog.svelte';
  import Icon from '../ui/Icon.svelte';

  let { release, updates, checking, onclose, onopen }: {
    release: Schema['ReleaseInfo'] | null;
    updates: Schema['UpdateStatus'] | null;
    checking: boolean;
    onclose: () => void;
    onopen: () => void;
  } = $props();
  let version = $derived(release?.current.version);
  let notes = $derived(release?.highlights);
  let updated = $derived(updates?.checked_at ? new Date(updates.checked_at).toLocaleString() : '');
  let opened = $state(false);
  $effect(() => {
    if (opened && release) onopen();
  });
</script>

<Dialog title={version ? `What's new in Folderhug ${version}` : 'Folderhug version details'} {onclose} onopen={() => { opened = true; }}>
  <div class="release-content">
    {#if notes}
      <p class="release-summary">{notes.summary}</p>
      <ul class="release-highlights">{#each notes.highlights as highlight, index (index)}<li>{highlight}</li>{/each}</ul>
    {:else}<p class="release-summary">No summary is included for this build.</p>{/if}
    <div class="release-check" role="status">
      {#if checking}<p>Checking for updates…</p>
      {:else if updates?.state === 'available' && updates.latest}<p><a href={releaseURL(updates.latest.version)} target="_blank" rel="noopener noreferrer">{updates.latest.version} is available<span class="sr-only"> (opens in a new tab)</span> <Icon name="external" size={14} /></a></p>
      {:else if updates?.state === 'current' && !updates.stale}<p>You're on the latest release.</p>
      {:else if updates?.state === 'ahead'}<p>This build is newer than the latest stable release.</p>
      {:else if updates?.state === 'disabled'}<p>Update checks are disabled on this server.</p>
      {:else if updates?.state === 'development'}<p>Development build. Update checks are skipped.</p>
      {:else}<p>Could not check for updates.</p>{/if}
      {#if updated}<p class="release-checked">{updates?.stale ? 'Last successful check' : 'Last checked'}: {updated}{updates?.stale ? '. This information may be out of date.' : ''}</p>{/if}
    </div>
    <div class="modal-actions release-actions">
      <a class="button secondary" href={releaseURL(version ?? 'dev')} target="_blank" rel="noopener noreferrer">{version && version !== 'dev' ? 'Full release notes' : 'All releases'}<span class="sr-only"> (opens in a new tab)</span><Icon name="external" size={16} /></a>
      <button class="button primary" onclick={onclose}>Close</button>
    </div>
  </div>
</Dialog>
