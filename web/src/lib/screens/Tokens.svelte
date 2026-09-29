<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, errorMessage, idPath, type Profile, type Schema } from '../api';
  import Dialog from '../ui/Dialog.svelte';
  import './activity.css';
  let { profiles, onnotice }: { profiles: Profile[]; onnotice: (message: string) => void } =
    $props();
  let tokens = $state<Schema['Token'][]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state('');
  let formError = $state('');
  let creating = $state(false);
  let name = $state('');
  let profileIds = $state<string[]>([]);
  let scopes = $state<Schema['TokenScope'][]>(['browse:read']);
  let expiry = $state('');
  let secret = $state('');
  let revoke = $state<Schema['Token'] | null>(null);
  let controller: AbortController | undefined;
  let creation: AbortController | undefined;
  let alive = true;
  const permissions: { scope: Schema['TokenScope']; label: string; description: string }[] = [
    {
      scope: 'browse:read',
      label: 'Browse and search',
      description: 'Read listings, indexed results and scan exports.',
    },
    {
      scope: 'content:read',
      label: 'Read file content',
      description: 'Download and preview files.',
    },
    {
      scope: 'scan:write',
      label: 'Manage scans',
      description: 'Start, pause, resume and cancel scans.',
    },
    {
      scope: 'downloads:write',
      label: 'Manage server downloads',
      description: 'Queue and manage downloads created by this token.',
    },
    {
      scope: 'credentials:unlock',
      label: 'Unlock profiles',
      description: 'Supply temporary source credentials.',
    },
  ];
  onMount(() => {
    void load();
  });
  onDestroy(() => {
    alive = false;
    controller?.abort();
    creation?.abort();
    secret = '';
  });
  async function load() {
    controller?.abort();
    const request = new AbortController();
    controller = request;
    loading = true;
    error = '';
    try {
      const result = await api<{ tokens: Schema['Token'][] }>('/tokens', {
        signal: request.signal,
      });
      if (!request.signal.aborted) tokens = result.tokens ?? [];
    } catch (e) {
      if (!request.signal.aborted) error = errorMessage(e);
    } finally {
      if (!request.signal.aborted) loading = false;
    }
  }
  function openCreate() {
    name = '';
    profileIds = [];
    scopes = ['browse:read'];
    expiry = '';
    secret = '';
    formError = '';
    creating = true;
  }
  function closeCreate() {
    if (busy) return;
    secret = '';
    creating = false;
    formError = '';
  }
  async function create() {
    formError = '';
    if (!name.trim() || !profileIds.length || !scopes.length) {
      formError = 'Enter a name and select at least one profile and permission.';
      return;
    }
    if (
      expiry &&
      (!Number.isFinite(new Date(expiry).getTime()) || new Date(expiry).getTime() <= Date.now())
    ) {
      formError = 'Choose a future expiration date and time.';
      return;
    }
    busy = true;
    creation = new AbortController();
    try {
      const result = await api<{ token: Schema['Token']; secret: string }>('/tokens', {
        method: 'POST',
        body: {
          name: name.trim(),
          profile_ids: profileIds,
          scopes,
          expires_at: expiry ? new Date(expiry).toISOString() : null,
        },
        signal: creation.signal,
      });
      if (!alive || creation.signal.aborted) return;
      secret = result.secret;
      tokens = [...tokens, result.token];
      onnotice('API token created. Copy it before closing this dialog.');
    } catch (e) {
      if (alive) formError = errorMessage(e);
    } finally {
      if (alive) busy = false;
    }
  }
  async function revokeToken() {
    if (!revoke) return;
    busy = true;
    formError = '';
    try {
      await api(`/tokens/${idPath(revoke.id)}`, { method: 'DELETE' });
      tokens = tokens.filter((token) => token.id !== revoke?.id);
      revoke = null;
      onnotice('API token revoked.');
    } catch (e) {
      formError = errorMessage(e);
    } finally {
      busy = false;
    }
  }
  async function copySecret() {
    try {
      await navigator.clipboard.writeText(secret);
      onnotice('API token copied.');
    } catch {
      formError = 'Copy is unavailable here. Select the token and copy it manually.';
    }
  }
  const profileName = (id: string) =>
    profiles.find((profile) => profile.id === id)?.name ?? 'Removed profile';
</script>

<section class="page-scroll activity-page" aria-label="API tokens">
  <div class="page-heading">
    <div>
      <h1>API tokens</h1>
      <p>Give an API client access to selected profiles and actions.</p>
    </div>
    <button class="button primary" onclick={openCreate} disabled={!profiles.length}
      >Create token</button
    >
  </div>
  {#if error}<div class="error-banner" role="alert">
      {error}<button class="button secondary" onclick={() => void load()}>Retry loading</button>
    </div>{/if}
  {#if loading}<p class="activity-note" role="status">Loading tokens…</p>{/if}
  {#if !profiles.length}<p class="activity-note">
      Add a profile before creating an API token.
    </p>{/if}
  {#if !loading && !error && !tokens.length}<p class="activity-empty">
      No API tokens. Create one when you need access from another client.
    </p>{/if}
  <div class="job-list">
    {#each tokens as token (token.id)}<article class="job" aria-label={token.name}>
        <div class="job-main">
          <strong>{token.name}</strong>
          <div class="job-meta">
            <span>Profiles: {token.profile_ids.map(profileName).join(', ') || 'None'}</span><span
              >{token.expires_at
                ? `${new Date(token.expires_at).getTime() <= Date.now() ? 'Expired' : 'Expires'} ${new Date(token.expires_at).toLocaleString()}`
                : 'No expiration'}</span
            >
          </div>
          <p class="activity-note">{token.scopes.join(', ')}</p>
        </div>
        <button
          class="button secondary"
          onclick={() => {
            revoke = token;
            formError = '';
          }}>Revoke</button
        >
      </article>{/each}
  </div>
  <p class="activity-note">
    Token secrets are displayed once. Revoking a token stops its future API access.
  </p>
</section>
{#if creating}
  <Dialog title={secret ? 'Copy your API token' : 'Create API token'} onclose={closeCreate}>
    {#if secret}
      <p class="activity-confirm-copy">
        Store this token somewhere safe. It will not be displayed again after you close this dialog.
      </p>
      <label class="form-field"
        >API token<textarea
          class="token-secret"
          readonly
          value={secret}
          spellcheck="false"
          autocomplete="off"></textarea></label
      >
      {#if formError}<p class="error-banner" role="alert">{formError}</p>{/if}
      <div class="modal-actions">
        <button class="button secondary" onclick={copySecret}>Copy token</button><button
          class="button primary"
          onclick={closeCreate}>Done</button
        >
      </div>
    {:else}
      <form
        onsubmit={(event) => {
          event.preventDefault();
          void create();
        }}
      >
        <label class="form-field"
          >Name<input required maxlength="128" bind:value={name} autocomplete="off" /></label
        >
        <fieldset class="token-options">
          <legend>Allowed profiles</legend>{#each profiles as profile (profile.id)}<label
              class="token-choice"
              ><input
                type="checkbox"
                value={profile.id}
                bind:group={profileIds}
              />{profile.name}</label
            >{/each}
        </fieldset>
        <fieldset class="token-options">
          <legend>Permissions</legend>{#each permissions as permission (permission.scope)}<label
              class="token-choice"
              ><input type="checkbox" value={permission.scope} bind:group={scopes} /><span
                >{permission.label}<small class="result-path">{permission.description}</small></span
              ></label
            >{/each}
        </fieldset>
        <label class="form-field"
          >Expires at (local time, optional)<input
            type="datetime-local"
            bind:value={expiry}
          /></label
        >
        <p class="field-help">Leave the expiration empty to keep this token valid until revoked.</p>
        {#if formError}<p class="error-banner" role="alert">{formError}</p>{/if}
        <div class="modal-actions">
          <button class="button secondary" type="button" disabled={busy} onclick={closeCreate}
            >Cancel</button
          ><button class="button primary" type="submit" disabled={busy}
            >{busy ? 'Creating…' : 'Create token'}</button
          >
        </div>
      </form>
    {/if}
  </Dialog>
{/if}
{#if revoke}
  <Dialog
    title="Revoke API token?"
    onclose={() => {
      if (!busy) revoke = null;
    }}
    ><p class="activity-confirm-copy">
      Revoke <strong>{revoke.name}</strong>? Clients using this token will lose API access. This
      cannot be undone.
    </p>
    {#if formError}<p class="error-banner" role="alert">{formError}</p>{/if}
    <div class="modal-actions">
      <button class="button secondary" disabled={busy} onclick={() => (revoke = null)}
        >Keep token</button
      ><button class="button primary" disabled={busy} onclick={revokeToken}>Revoke token</button>
    </div></Dialog
  >
{/if}
