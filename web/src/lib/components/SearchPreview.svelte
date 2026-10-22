<script lang="ts">
  import { api, endpoint, idPath, type Entry, type Schema } from '../api';
  import FileMark from '../ui/FileMark.svelte';

  let { entry, enabled }: { entry: Entry; enabled: boolean } = $props();
  let host = $state<HTMLSpanElement>();
  let thumbnail = $state('');
  const profileId = $derived(entry.profile_id);
  const entryId = $derived(entry.id);
  const canPreview = $derived(enabled && entry.category === 'image');

  $effect(() => {
    const profile = profileId, id = entryId, target = host;
    const allowed = canPreview;
    thumbnail = '';
    if (!target || !allowed) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let visible = false, busy = false, done = false;
    const observer = new IntersectionObserver(([item]) => {
      visible = item?.isIntersecting ?? false;
      if (visible) void request();
      else clearTimeout(timer);
    }, { root: target.closest('.page-scroll'), rootMargin: '100px 0px' });
    observer.observe(target);

    async function request() {
      if (!visible || busy || done || controller.signal.aborted) return;
      clearTimeout(timer);
      busy = true;
      let pending = false;
      try {
        const result = await api<{ thumbnails: Schema['Thumbnail'][] }>(`/profiles/${idPath(profile)}/thumbnails`, {
          method: 'POST', body: { entry_ids: [id] }, signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        const item = result.thumbnails.find((item) => item.entry_id === id);
        if (item?.state === 'ready') thumbnail = endpoint(`/thumbnails/${idPath(item.id)}`);
        pending = item?.state === 'pending';
        done = !pending;
      } catch {
        // Keep the file icon when a preview cannot be generated.
        done = true;
      } finally {
        busy = false;
        if (pending && visible && !controller.signal.aborted) timer = setTimeout(() => void request(), 2000);
      }
    }

    return () => {
      observer.disconnect();
      controller.abort();
      clearTimeout(timer);
    };
  });
</script>

<span class="search-preview" bind:this={host} aria-hidden="true">
  <FileMark {entry} large {thumbnail} />
</span>

<style>
  .search-preview {
    display: flex;
    flex: 0 0 64px;
    width: 64px;
    height: 64px;
  }
  .search-preview :global(.file-mark.large) {
    width: 64px;
    height: 64px;
  }
</style>
