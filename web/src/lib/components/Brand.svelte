<script lang="ts">
  let { login = false }: { login?: boolean } = $props();
</script>

{#snippet wordmark()}
  <img class="wordmark-light" src="/brand/folderhug-wordmark-light.svg" alt="Folderhug" width="128" height="31" />
  <img class="wordmark-dark" src="/brand/folderhug-wordmark-dark.svg" alt="Folderhug" width="128" height="31" />
{/snippet}

<div class="folderhug-brand" class:login-brand={login}>
  <img class="folderhug-mark" src="/brand/folderhug-mark.svg" alt="" width="52" height="52" />
  {#if login}
    <h1>{@render wordmark()}</h1>
  {:else}
    <span class="nav-label folderhug-label">
      {@render wordmark()}
      <small>Your HTTP folders</small>
    </span>
  {/if}
</div>

<style>
  .folderhug-brand {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
  }
  .folderhug-mark {
    flex-shrink: 0;
    display: block;
  }
  .folderhug-label {
    min-width: 0;
  }
  .folderhug-label img {
    display: block;
    width: 128px;
    max-width: 100%;
    height: auto;
  }
  .folderhug-label small {
    margin-top: 0;
  }
  .wordmark-dark,
  .folderhug-label .wordmark-dark {
    display: none;
  }
  :global([data-theme='dark']) .wordmark-light {
    display: none;
  }
  :global([data-theme='dark']) .wordmark-dark {
    display: block;
  }
  .login-brand {
    flex-direction: column;
    gap: 0;
    margin-bottom: 8px;
  }
  .login-brand .folderhug-mark {
    width: 104px;
    height: 104px;
  }
  .login-brand h1 {
    margin: 0;
  }
  .login-brand h1 img {
    width: 170px;
    height: auto;
  }
  @media (min-width: 801px) and (max-width: 1150px) {
    .folderhug-brand { gap: 4px; }
    .folderhug-mark { width: 46px; height: 46px; }
    .folderhug-label img { width: 116px; }
  }
</style>
