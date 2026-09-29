<script lang="ts">
  import { onDestroy, tick, untrack } from 'svelte';
  import { api, errorMessage, idPath, type Profile, type Schema } from '../api';
  import Dialog from '../ui/Dialog.svelte';

  let {
    profile,
    mode,
    onclose,
    onchanged,
    onnotice,
  }: {
    profile?: Profile;
    mode: 'edit' | 'create' | 'unlock' | 'remove';
    onclose: () => void;
    onchanged: (profile?: Profile) => void;
    onnotice: (message: string) => void;
  } = $props();

  const initial = untrack(() => profile);
  let current = $state<Profile | undefined>(initial);
  let name = $state(initial?.name ?? '');
  let rootURL = $state(initial?.root_url ?? '');
  let authType = $state<'none' | 'basic'>(initial?.auth_type ?? 'none');
  let allowHTTP = $state(initial?.allow_insecure_http ?? false);
  let previews = $state(initial?.previews_enabled ?? true);
  let connections = $state<1 | 2 | 4>(initial?.connections_per_file ?? 1);
  let username = $state('');
  let password = $state('');
  let remember = $state(false);
  let replaceCredentials = $state(false);
  let clearCredentials = $state(false);
  let sourceConfirmed = $state(false);
  let leaseSeconds = $state(3600);
  let busy = $state(false);
  let removing = $state(untrack(() => mode === 'remove'));
  let error = $state('');
  let result = $state('');
  let keepButton = $state<HTMLButtonElement>();
  let removeButton = $state<HTMLButtonElement>();
  let closed = false;
  const testController = new AbortController();
  const sourceChanged = $derived(
    Boolean(
      current && (rootURL.trim() !== current.root_url || allowHTTP !== current.allow_insecure_http),
    ),
  );
  const credentialFields = $derived(
    mode === 'unlock' ||
      (authType === 'basic' && (!current || current.auth_type !== 'basic' || replaceCredentials)),
  );
  const title = $derived(
    removing
      ? 'Remove profile'
      : mode === 'unlock'
        ? `Unlock ${current?.name ?? 'profile'}`
        : current
          ? 'Edit profile'
          : 'Add profile',
  );

  function clearSecrets() {
    username = '';
    password = '';
    remember = false;
  }
  function close() {
    closed = true;
    clearSecrets();
    testController.abort();
    onclose();
  }
  onDestroy(() => {
    closed = true;
    clearSecrets();
    testController.abort();
  });

  function reportFailure(failure: unknown, prefix = '') {
    const message = errorMessage(failure);
    if (!message) return;
    if (closed) onnotice(prefix + message);
    else error = prefix + message;
  }

  async function confirmRemoval(show: boolean) {
    removing = show;
    error = '';
    if (show) clearSecrets();
    await tick();
    if (!closed) (show ? keepButton : removeButton)?.focus();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy) return;
    const test = (event.submitter as HTMLButtonElement | null)?.value === 'test';
    busy = true;
    error = '';
    result = '';
    let saved = false;
    try {
      if (mode === 'unlock') {
        if (!current) throw new Error('Missing profile');
        const lease = await api<{ expires_at: string }>(`/profiles/${idPath(current.id)}/unlock`, {
          method: 'POST',
          body: {
            username,
            password,
            lease_seconds: leaseSeconds,
          } satisfies Schema['UnlockRequest'],
        });
        clearSecrets();
        const unlocked = { ...current, credential_state: 'temporary' as const };
        onchanged(unlocked);
        onnotice(`Profile unlocked until ${new Date(lease.expires_at).toLocaleTimeString()}.`);
        if (!closed) close();
        return;
      }
      if (sourceChanged && !sourceConfirmed) {
        error = 'Confirm the source change before saving.';
        return;
      }
      const body: Schema['ProfilePatch'] = {
        name: name.trim(),
        root_url: rootURL.trim(),
        auth_type: authType,
        allow_insecure_http: allowHTTP,
        previews_enabled: previews,
        connections_per_file: connections,
      };
      if (authType === 'basic' && credentialFields)
        body.credentials = { username, password, remember };
      else if (current && clearCredentials) body.credentials = null;
      const updated = await api<Profile>(
        current ? `/profiles/${idPath(current.id)}` : '/profiles',
        {
          method: current ? 'PATCH' : 'POST',
          body,
        },
      );
      saved = true;
      current = updated;
      rootURL = updated.root_url;
      name = updated.name;
      replaceCredentials = false;
      clearCredentials = false;
      sourceConfirmed = false;
      clearSecrets();
      onchanged(updated);
      if (closed) {
        onnotice('Profile saved.');
        return;
      }
      if (!test) {
        onnotice('Profile saved.');
        close();
        return;
      }
      result = 'Profile saved. Testing the directory listing…';
      const diagnosis = await api<Schema['ConnectionTest']>(
        `/profiles/${idPath(updated.id)}/test`,
        {
          method: 'POST',
          signal: testController.signal,
        },
      );
      const formats = {
        apache_table: 'Apache table',
        apache_pre: 'Apache preformatted',
        nginx: 'nginx',
      };
      result = `Ready. ${formats[diagnosis.listing_format]} listing detected. ${
        diagnosis.range_observation === 'not_tested'
          ? 'Range support was not tested.'
          : diagnosis.range_observation === 'supported_for_sample'
            ? 'The sampled file supports byte ranges; other files may differ.'
            : 'The sampled file did not support byte ranges.'
      }`;
    } catch (failure) {
      result = '';
      reportFailure(failure, saved ? 'Profile saved. Connection test failed: ' : '');
    } finally {
      busy = false;
    }
  }

  async function removeProfile() {
    if (!current || busy) return;
    busy = true;
    error = '';
    try {
      await api<void>(`/profiles/${idPath(current.id)}`, { method: 'DELETE' });
      clearSecrets();
      onchanged();
      onnotice('Profile removed. Completed server files were kept.');
      if (!closed) close();
    } catch (failure) {
      reportFailure(failure);
    } finally {
      busy = false;
    }
  }

  async function lockProfile() {
    if (!current || current.credential_state !== 'temporary' || busy) return;
    busy = true;
    error = '';
    try {
      await api<void>(`/profiles/${idPath(current.id)}/lock`, { method: 'POST' });
      clearSecrets();
      const updated = await api<Profile>(`/profiles/${idPath(current.id)}`);
      onchanged(updated);
      onnotice(
        updated.credential_state === 'remembered'
          ? 'Temporary credentials cleared. This profile still has a remembered password.'
          : 'Temporary credentials cleared. Active requests using them were cancelled.',
      );
      if (!closed) close();
    } catch (failure) {
      reportFailure(failure);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog {title} onclose={close}>
  {#if removing}
    <p>Remove <strong>{current?.name}</strong> from Folderhug?</p>
    <p class="field-help">
      Its saved credentials, cached listings, index, scans, and pins will be removed. Unfinished
      server downloads cannot continue. Completed server files are kept. Files on the source are
      unchanged.
    </p>
    {#if error}<p class="form-error" role="alert">{error}</p>{/if}
    <div class="modal-actions">
      <button
        bind:this={keepButton}
        type="button"
        class="button secondary"
        disabled={busy}
        onclick={() => mode === 'remove' ? close() : void confirmRemoval(false)}>Keep profile</button
      >
      <button type="button" class="button danger" disabled={busy} onclick={removeProfile}
        >{busy ? 'Removing…' : 'Remove profile'}</button
      >
    </div>
  {:else}
    <form
      onsubmit={submit}
      oninput={() => {
        result = '';
        error = '';
      }}
      autocomplete="off"
    >
      <fieldset class="profile-fields" disabled={busy}>
        <legend class="sr-only"
          >{mode === 'unlock' ? 'Temporary credentials' : 'Profile settings'}</legend
        >
        {#if mode !== 'unlock'}
          <label class="form-field"
            >Name<input name="profile-name" bind:value={name} required maxlength="128" /></label
          >
          <label class="form-field"
            >Root URL<input
              name="profile-root"
              type="url"
              bind:value={rootURL}
              oninput={() => {
                sourceConfirmed = false;
              }}
              required
              maxlength="4096"
              spellcheck="false"
              aria-describedby="profile-url-help"
            /></label
          >
          <p class="field-help" id="profile-url-help">
            Use the URL of a directory listing. Keep usernames and passwords in the credential
            fields below.
          </p>
          <label class="form-field"
            >Authentication<select
              bind:value={authType}
              onchange={() => {
                clearSecrets();
                clearCredentials = false;
                replaceCredentials = false;
              }}
            >
              <option value="none">None</option><option value="basic"
                >Username and password (Basic)</option
              >
            </select></label
          >
          {#if current?.auth_type === 'basic' && authType === 'basic'}
            <p class="field-help">
              Credentials: {current.credential_state === 'remembered'
                ? 'stored encrypted on this server'
                : current.credential_state === 'temporary'
                  ? 'temporary for this session'
                  : 'password needed'}. Saved passwords are never shown here.
            </p>
            <label class="check-field"
              ><input
                type="checkbox"
                bind:checked={replaceCredentials}
                onchange={() => {
                  clearSecrets();
                  clearCredentials = false;
                }}
              /> Replace credentials</label
            >
            {#if !replaceCredentials}
              <label class="check-field"
                ><input type="checkbox" bind:checked={clearCredentials} /> Clear stored and temporary
                credentials</label
              >
            {/if}
          {/if}
        {:else}
          <p class="modal-intro">
            Credentials stay in server memory for this session. Logout, lease expiry, or an app
            restart clears them.
          </p>
        {/if}
        {#if credentialFields}
          <label class="form-field"
            >Username<input
              name="source-username"
              autocomplete="off"
              bind:value={username}
              maxlength="1024"
              spellcheck="false"
            /></label
          >
          <label class="form-field"
            >Password<input
              name="source-password"
              type="password"
              autocomplete="new-password"
              bind:value={password}
              maxlength="4096"
            /></label
          >
          {#if mode === 'unlock'}
            <label class="form-field"
              >Unlock for<select bind:value={leaseSeconds}>
                <option value={900}>15 minutes</option><option value={3600}>1 hour</option><option
                  value={14400}>4 hours</option
                ><option value={28800}>8 hours</option>
              </select></label
            >
            <p class="field-help">The unlock ends sooner if your owner session expires.</p>
          {:else}
            <label class="check-field"
              ><input type="checkbox" bind:checked={remember} /> Remember password</label
            >
            <p class="field-help">
              Store this password encrypted on this server. If unchecked, logout or an app restart
              clears it.
            </p>
          {/if}
        {/if}
        {#if mode !== 'unlock'}
          <details class="advanced-settings">
            <summary>Advanced connection settings</summary>
            <label class="check-field"
              ><input
                type="checkbox"
                bind:checked={allowHTTP}
                onchange={() => {
                  sourceConfirmed = false;
                }}
              /> Allow insecure HTTP</label
            >
            <p class="field-help">
              HTTP sends source credentials and file content without encryption.
            </p>
            <label class="check-field"
              ><input type="checkbox" bind:checked={previews} /> Enable media previews</label
            >
            <label class="form-field"
              >Connections per file<select bind:value={connections}>
                <option value={1}>1 connection</option><option value={2}>2 connections</option
                ><option value={4}>4 connections</option>
              </select></label
            >
            <p class="field-help">
              Server downloads use extra connections only when the source passes range and
              consistency checks.
            </p>
          </details>
          {#if sourceChanged}
            <div class="source-warning">
              <p>
                Changing the source URL or HTTP setting clears credentials and invalidates cached
                listings and indexed results. Active source requests and jobs may stop.
              </p>
              <label class="check-field"
                ><input type="checkbox" bind:checked={sourceConfirmed} required /> Confirm this source
                change</label
              >
            </div>
          {/if}
        {/if}
      </fieldset>
      {#if error}<p class="form-error" role="alert">{error}</p>{/if}
      {#if result}<p class="form-result" role="status">{result}</p>{/if}
      <div class="modal-actions">
        <button type="button" class="button secondary" onclick={close}
          >{result ? 'Close' : 'Cancel'}</button
        >
        {#if mode !== 'unlock'}<button
            type="submit"
            value="test"
            class="button secondary"
            disabled={busy}>Save and test</button
          >{/if}
        <button type="submit" value="save" class="button primary" disabled={busy}
          >{busy ? 'Working…' : mode === 'unlock' ? 'Unlock profile' : 'Save profile'}</button
        >
      </div>
    </form>
    {#if current && mode !== 'unlock'}
      <div class="profile-maintenance">
        {#if current.credential_state === 'temporary'}<button
            type="button"
            class="button secondary"
            disabled={busy}
            onclick={lockProfile}>Lock profile</button
          >{/if}
        <button
          bind:this={removeButton}
          type="button"
          class="button danger"
          disabled={busy}
          onclick={() => confirmRemoval(true)}>Remove profile…</button
        >
      </div>
    {/if}
  {/if}
</Dialog>

<style>
  .profile-fields {
    border: 0;
    padding: 0;
    margin: 0;
    min-width: 0;
  }
  .profile-fields :global(.form-field) {
    margin-block: 12px;
  }
  .check-field {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 44px;
  }
  .check-field input {
    flex: 0 0 auto;
  }
  .advanced-settings {
    margin-top: 16px;
  }
  summary {
    cursor: pointer;
    min-height: 44px;
    align-content: center;
  }
  .source-warning {
    margin-top: 16px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .profile-maintenance {
    margin-top: 20px;
    padding-top: 16px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .modal-actions {
    flex-wrap: wrap;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
