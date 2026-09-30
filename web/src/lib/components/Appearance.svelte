<script lang="ts">
  import { onMount } from 'svelte';
  import type { Preferences } from '../api';
  import { errorMessage } from '../api';
  import Icon from '../ui/Icon.svelte';
  import Dialog from '../ui/Dialog.svelte';

  let {
    preferences,
    onsave,
    onclose,
  }: {
    preferences: Preferences;
    onsave: (patch: Partial<Preferences>) => Promise<void>;
    onclose: () => void;
  } = $props();

  const modes = [
    { id: 'system', name: 'System', icon: 'monitor' },
    { id: 'light', name: 'Light', icon: 'sun' },
    { id: 'dark', name: 'Dark', icon: 'moon' },
  ] as const;
  const accents = [
    { id: 'blue', label: 'Blue', light: '#285ED1', dark: '#8CB3FF' },
    { id: 'teal', label: 'Teal', light: '#087F7A', dark: '#73D1C6' },
    { id: 'violet', label: 'Violet', light: '#7651BD', dark: '#C4A5F5' },
    { id: 'rose', label: 'Rose', light: '#B03E65', dark: '#F3A2BC' },
    { id: 'amber', label: 'Amber', light: '#AD5426', dark: '#EAB079' },
    { id: 'graphite', label: 'Graphite', light: '#536173', dark: '#B4C0D0' },
  ] as const;
  let systemDark = $state(false);
  let saving = $state(false);
  let error = $state('');
  const dark = $derived(
    preferences.theme === 'dark' || (preferences.theme === 'system' && systemDark),
  );

  onMount(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const update = () => {
      systemDark = media.matches;
    };
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  });

  async function save(patch: Partial<Preferences>) {
    if (saving) return;
    saving = true;
    error = '';
    try {
      await onsave(patch);
    } catch (failure) {
      error = errorMessage(failure);
    } finally {
      saving = false;
    }
  }

  function close() {
    if (!saving) onclose();
  }
</script>

<Dialog title="Appearance" onclose={close}>
  <p class="modal-intro">Choose a theme and accent color.</p>
  <fieldset class="appearance-modes" disabled={saving}>
    <legend>Appearance</legend>
    {#each modes as option (option.id)}
      <label class:chosen={preferences.theme === option.id}>
        <input
          type="radio"
          name="appearance-theme"
          value={option.id}
          checked={preferences.theme === option.id}
          onchange={() => save({ theme: option.id })}
        />
        <Icon name={option.icon} size={22} /><span>{option.name}</span>
        {#if preferences.theme === option.id}<span class="mode-check"
            ><Icon name="check" size={13} /></span
          >{/if}
      </label>
    {/each}
  </fieldset>
  <p class="field-help">System follows your device's light or dark setting.</p>
  <fieldset class="accent-options" disabled={saving}>
    <legend>Accent color</legend>
    {#each accents as accent (accent.id)}
      <label>
        <input
          type="radio"
          name="appearance-accent"
          value={accent.id}
          checked={preferences.accent === accent.id}
          onchange={() => save({ accent: accent.id })}
        />
        <span
          class="color-swatch"
          class:chosen={preferences.accent === accent.id}
          style={`--color:${dark ? accent.dark : accent.light}`}
        >
          {#if preferences.accent === accent.id}<Icon name="check" size={17} />{/if}
        </span>
        <span>{accent.label}</span>
      </label>
    {/each}
  </fieldset>
  <label class="density-setting">
    <span
      ><strong>Compact rows</strong><span class="density-help"
        >Fit more files in list and details views.</span
      ></span
    >
    <input
      type="checkbox"
      aria-label="Compact rows"
      checked={preferences.density === 'compact'}
      disabled={saving}
      onchange={(event) =>
        save({ density: event.currentTarget.checked ? 'compact' : 'comfortable' })}
    />
  </label>
  <p class="field-help appearance-help" role="status">
    {saving ? 'Saving appearance…' : 'Changes are saved to your owner account.'}
  </p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  <div class="modal-actions">
    <button type="button" class="button primary" disabled={saving} onclick={close}>Done</button>
  </div>
</Dialog>

<style>
  .density-help {
    display: block;
    margin-top: 4px;
    color: var(--muted);
    font-size: 12px;
  }
  .density-setting {
    min-height: 44px;
    cursor: pointer;
  }
</style>
