<script lang="ts">
  import { onMount, tick, type Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { dialogOpener, openDialogs } from './focus';
  let {
    title,
    onclose,
    children,
    wide = false,
    onopen,
  }: { title: string; onclose: () => void; children: Snippet; wide?: boolean; onopen?: () => void } = $props();
  let dialog: HTMLDialogElement;
  const titleId = $props.id();
  onMount(() => {
    const opener = dialogOpener();
    dialog.showModal();
    openDialogs.update((count) => count + 1);
    onopen?.();
    return () => {
      dialog.close();
      openDialogs.update((count) => count - 1);
      void tick().then(() => {
        if (opener?.isConnected) opener.focus();
      });
    };
  });
</script>

<dialog
  bind:this={dialog}
  class="modal"
  class:media-modal={wide}
  aria-labelledby={titleId}
  oncancel={(event) => {
    event.preventDefault();
    onclose();
  }}
>
  <div class="modal-heading">
    <h2 id={titleId}>{title}</h2>
    <button class="icon-button" aria-label="Close dialog" onclick={onclose}
      ><Icon name="x" /></button
    >
  </div>
  {@render children()}
</dialog>
