<script lang="ts">
  import { untrack } from 'svelte';
  import { randomUUID } from '../uuid';
  import { localDate, errorMessage, type Schema, type Preferences } from '../api';
  import Dialog from '../ui/Dialog.svelte';
  let {
    filters,
    preferences,
    profileId,
    onapply,
    onsave,
    onclose,
  }: {
    filters: Schema['PresetFilters'];
    preferences: Preferences;
    profileId: string;
    onapply: (filters: Schema['PresetFilters']) => void;
    onsave: (patch: Partial<Preferences>) => Promise<void>;
    onclose: () => void;
  } = $props();
  const initial = untrack(() => filters);
  let value = $state({ ...initial });
  let after = $state(localDate(initial.modified_after));
  let before = $state(localDate(initial.modified_before));
  let presetName = $state('');
  let editing = $state('');
  let error = $state('');
  let busy = $state(false);
  function current(): Schema['PresetFilters'] {
    const result = { ...value };
    if (after) result.modified_after = new Date(after).toISOString();
    else delete result.modified_after;
    if (before) result.modified_before = new Date(before).toISOString();
    else delete result.modified_before;
    return result;
  }
  async function save() {
    busy = true;
    try {
      await onsave({
        filter_presets: editing
          ? preferences.filter_presets.map((preset) =>
              preset.id === editing ? { ...preset, name: presetName.trim() } : preset,
            )
          : [
              ...preferences.filter_presets,
              {
                id: randomUUID(),
                name: presetName.trim(),
                profile_id: profileId,
                filters: current(),
              },
            ],
      });
      presetName = '';
      editing = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog title="Folder filters" {onclose}>
  <form
    onsubmit={(event) => {
      event.preventDefault();
      onapply(current());
      onclose();
    }}
  >
    <p class="modal-intro">
      These filters apply to the current folder. Use Find files to look through subfolders.
    </p>
    {#if error}<p class="error-banner" role="alert">{error}</p>{/if}
    <label class="form-field"
      >Extensions, separated by commas<input
        bind:value={value.extensions}
        placeholder="mp4, mkv"
        maxlength="512"
      /></label
    >
    <div class="form-grid">
      <label class="form-field"
        >Minimum size (bytes)<input type="number" min="0" bind:value={value.min_size} /></label
      ><label class="form-field"
        >Maximum size (bytes)<input
          type="number"
          min={value.min_size ?? 0}
          bind:value={value.max_size}
        /></label
      >
    </div>
    <div class="form-grid">
      <label class="form-field"
        >Modified after<input type="datetime-local" bind:value={after} /></label
      ><label class="form-field"
        >Modified before<input type="datetime-local" bind:value={before} /></label
      >
    </div>
    <p class="field-help">Date filters exclude files whose source time zone is unknown.</p>
    <label class="check-label"
      ><input
        type="checkbox"
        checked={value.show_folders !== false}
        onchange={(event) => (value.show_folders = event.currentTarget.checked)}
      /> Show folders</label
    >
    <div class="modal-actions">
      <button
        type="button"
        class="button secondary"
        onclick={() => {
          onapply({});
          onclose();
        }}>Clear filters</button
      ><button class="button primary">Apply filters</button>
    </div>
  </form>
  <div class="preset-editor">
    <h3>Saved filters</h3>
    {#each preferences.filter_presets.filter((p) => !p.profile_id || p.profile_id === profileId) as preset (preset.id)}<div
        class="preset-row"
      >
        <button
          class="button secondary"
          onclick={() => {
            onapply(preset.filters);
            onclose();
          }}>{preset.name}</button
        ><button
          class="button secondary"
          disabled={busy}
          aria-label={`Rename preset ${preset.name}`}
          onclick={() => {
            editing = preset.id;
            presetName = preset.name;
          }}>Rename</button
        ><button
          class="button secondary"
          disabled={busy}
          aria-label={`Remove preset ${preset.name}`}
          onclick={async () => {
            busy = true;
            try {
              await onsave({
                filter_presets: preferences.filter_presets.filter((p) => p.id !== preset.id),
              });
            } catch (e) {
              error = errorMessage(e);
            } finally {
              busy = false;
            }
          }}>Remove</button
        >
      </div>{/each}
    <label class="form-field">Preset name<input bind:value={presetName} maxlength="128" /></label
    ><button
      class="button secondary"
      disabled={busy || !presetName.trim() || (!editing && preferences.filter_presets.length >= 50)}
      onclick={save}>{editing ? 'Rename preset' : 'Save current filters'}</button
    >
  </div>
</Dialog>
