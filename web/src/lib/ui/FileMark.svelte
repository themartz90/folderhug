<script lang="ts">
  import Icon from './Icon.svelte';
  import type { Entry } from '../api';
  let {
    entry,
    large = false,
    thumbnail = '',
  }: { entry: Entry; large?: boolean; thumbnail?: string } = $props();
  let failed = $state(false);
  $effect(() => {
    void thumbnail;
    failed = false;
  });
</script>

<span
  class="file-mark"
  class:large
  class:folder={entry.kind === 'directory'}
  class:image={entry.category === 'image'}
  class:video={entry.category === 'video'}
>
  {#if thumbnail && !failed}<img
      src={thumbnail}
      alt=""
      loading="lazy"
      onerror={() => (failed = true)}
    />
  {:else}<Icon
      name={entry.kind === 'directory'
        ? 'folder'
        : ['image', 'video', 'audio', 'archive'].includes(entry.category)
          ? entry.category
          : 'file'}
      size={large ? 44 : 22}
    />{/if}
</span>
