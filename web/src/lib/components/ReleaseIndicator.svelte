<script lang="ts">
  import type { Schema } from '../api';
  import { releaseURL } from '../releases';
  import Icon from '../ui/Icon.svelte';

  let { release, updates, onopen }: {
    release: Schema['ReleaseInfo'] | null;
    updates: Schema['UpdateStatus'] | null;
    onopen: () => void;
  } = $props();
  let version = $derived(release?.current.version);
  let update = $derived(updates?.state === 'available' ? updates.latest : null);
</script>

<div class="release-indicator">
  <button class="appearance-link release-version" title={version ? `What's new in Folderhug ${version}` : 'Folderhug version details'} aria-label={version ? `What's new in Folderhug ${version}` : 'Folderhug version details'} onclick={onopen}>
    <Icon name="info" size={19} /><span class="nav-label">Folderhug {version ?? ''}</span>
  </button>
  {#if update}
    <a class="appearance-link release-update" href={releaseURL(update.version)} target="_blank" rel="noopener noreferrer" title={`Update available: ${update.version} (opens in a new tab)`} aria-label={`Update available: ${update.version} (opens in a new tab)`}>
      <Icon name="external" size={19} /><span class="nav-label">Update available</span>
    </a>
  {/if}
</div>
