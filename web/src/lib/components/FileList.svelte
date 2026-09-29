<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { formatBytes, formatDate, type Entry } from '../api';
  import { filenameParts, type View } from '../explorer';
  import FileMark from '../ui/FileMark.svelte';
  import Icon from '../ui/Icon.svelte';
  let {
    entries,
    selected,
    view,
    compact,
    gridSize,
    total,
    busy,
    hasMore,
    initialScroll = 0,
    thumbnails,
    onselect,
    onopen,
    ondetails,
    onmore,
    onvisible,
    onscroll,
    onsort,
    onselectall,
    onclear,
    onup,
  }: {
    entries: Entry[];
    selected: Record<string, Entry>;
    view: View;
    compact: boolean;
    gridSize: 'small' | 'medium' | 'large';
    total: number;
    busy: boolean;
    hasMore: boolean;
    initialScroll?: number;
    thumbnails: Record<string, string>;
    onselect: (id: string, range: boolean) => void;
    onopen: (entry: Entry) => void;
    ondetails: (entry: Entry) => void;
    onmore: () => void;
    onvisible: (entries: Entry[]) => void;
    onscroll: (position: number) => void;
    onsort: (sort: View['sort']) => void;
    onselectall: () => void;
    onclear: () => void;
    onup: () => void;
  } = $props();
  let scroller: HTMLDivElement;
  let top = $state(untrack(() => initialScroll));
  let height = $state(600);
  let width = $state(900);
  let focused = $state('');
  let phoneColumns = $state(false);
  let narrowColumns = $state(false);
  let columnSpan = $derived(phoneColumns ? 3 : view.view === 'compact' ? 4 : narrowColumns ? 5 : 6);
  let rowHeight = $derived(
    view.view === 'grid'
      ? { small: 180, medium: 210, large: 260 }[gridSize]
      : width < 540
        ? 66
        : compact || view.view === 'compact'
          ? 44
          : 55,
  );
  let columns = $derived(
    view.view === 'grid'
      ? Math.max(
          width < 540 && gridSize === 'large' ? 1 : 2,
          Math.floor((width - 32) / { small: 160, medium: 205, large: 280 }[gridSize]),
        )
      : 1,
  );
  let start = $derived(Math.max(0, Math.floor(top / rowHeight) - 3) * columns);
  let end = $derived(
    Math.min(entries.length, (Math.ceil((top + height) / rowHeight) + 3) * columns),
  );
  let indices = $derived.by(() => {
    const rows = Array.from({ length: Math.max(0, end - start) }, (_, i) => start + i);
    const index = entries.findIndex((entry) => entry.id === focused);
    if (index >= 0 && !rows.includes(index)) rows.push(index);
    return rows.sort((a, b) => a - b);
  });
  onMount(() => {
    const phone = matchMedia('(max-width: 540px)');
    const narrow = matchMedia('(max-width: 1150px)');
    const updateColumns = () => {
      phoneColumns = phone.matches;
      narrowColumns = narrow.matches;
    };
    updateColumns();
    phone.addEventListener('change', updateColumns);
    narrow.addEventListener('change', updateColumns);
    const observer = new ResizeObserver(([entry]) => {
      if (entry) {
        height = entry.contentRect.height;
        width = entry.contentRect.width;
      }
    });
    observer.observe(scroller);
    scroller.scrollTop = initialScroll;
    return () => {
      observer.disconnect();
      phone.removeEventListener('change', updateColumns);
      narrow.removeEventListener('change', updateColumns);
    };
  });
  $effect(() => {
    void view.view;
    const visible = entries.slice(start, end);
    untrack(() => onvisible(visible));
  });
  function scroll() {
    top = scroller.scrollTop;
    onscroll(top);
    if (hasMore && !busy && top + height >= scroller.scrollHeight - 300) onmore();
  }
  function key(event: KeyboardEvent) {
    if ((event.target as HTMLElement).matches('input,select,textarea')) return;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
      event.preventDefault();
      onselectall();
    } else if (event.altKey && event.key === 'ArrowUp') {
      event.preventDefault();
      onup();
    } else if (event.key === 'Escape') onclear();
    else if (
      event.key === ' ' &&
      (event.target as HTMLElement).closest<HTMLElement>('[data-entry-id]')
    ) {
      event.preventDefault();
      onselect(
        (event.target as HTMLElement).closest<HTMLElement>('[data-entry-id]')!.dataset.entryId!,
        event.shiftKey,
      );
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (The scroll region is keyboard focusable; shortcuts are scoped here without imposing grid navigation.) -->
<div
  class="file-scroll"
  bind:this={scroller}
  onscroll={scroll}
  onkeydown={key}
  onclick={(event) => {
    const target = event.target as HTMLElement;
    if (target.closest('button,input,label')) return;
    const row = target.closest<HTMLElement>('[data-entry-row]');
    if (row?.dataset.entryRow) {
      onselect(row.dataset.entryRow, event.shiftKey);
      scroller.focus();
    }
  }}
  tabindex="0"
  role="region"
  aria-label="Files"
  aria-busy={busy}
>
  {#if view.view === 'grid'}
    <ul
      class="virtual-grid"
      class:multiline-names={gridSize !== 'small'}
      style:height={`${Math.ceil(entries.length / columns) * rowHeight}px`}
      style:--grid-height={`${rowHeight}px`}
    >
      {#each indices as index (entries[index]!.id)}
        {@const entry = entries[index]!}
        {@const parts = filenameParts(entry)}
        <li
          class="file-card"
          data-entry-row={entry.id}
          class:selected={!!selected[entry.id]}
          style:position="absolute"
          style:top={`${Math.floor(index / columns) * rowHeight}px`}
          style:left={`${((index % columns) * 100) / columns}%`}
          style:width={`${100 / columns}%`}
          onfocusin={() => (focused = entry.id)}
          onfocusout={() => (focused = '')}
          oncontextmenu={(event) => {
            event.preventDefault();
            ondetails(entry);
          }}
        >
          <label class="grid-check"
            ><input
              type="checkbox"
              aria-label={`Select ${entry.name}`}
              checked={!!selected[entry.id]}
              onclick={(event) => onselect(entry.id, event.shiftKey)}
            /></label
          >
          <button
            class="grid-open"
            data-entry-id={entry.id}
            aria-label={entry.name}
            onclick={(event) => {
              if (event.ctrlKey || event.metaKey || event.shiftKey)
                onselect(entry.id, event.shiftKey);
              else onopen(entry);
            }}
            ><span class="card-art"
              ><FileMark {entry} large thumbnail={thumbnails[entry.id] ?? ''} /></span
            ><span class="card-caption"
              ><span class="file-name"><span>{parts[0]}</span><span>{parts[1]}</span></span><small
                >{entry.kind === 'directory'
                  ? 'Folder'
                  : `${entry.size != null && !entry.size_exact ? '≈ ' : ''}${formatBytes(entry.size)}`}</small
              ></span
            ></button
          >
          <button
            class="card-info icon-button"
            aria-label={`Details for ${entry.name}`}
            onclick={() => ondetails(entry)}><Icon name="more" size={17} /></button
          >
        </li>
      {/each}
    </ul>
  {:else}
    <table
      class:compact-list={view.view === 'compact'}
      aria-label="Folder contents"
      aria-rowcount={total + 1}
    >
      <thead
        ><tr
          ><th class="check-cell"><span class="sr-only">Selection</span></th>
          {#each [['name', 'Name'], ['type', 'Type'], ['size', 'Size'], ['modified', 'Modified']] as column (column[0])}
            <th
              class={column[0] === 'name'
                ? 'name-cell'
                : column[0] === 'modified'
                  ? 'date-cell'
                  : `${column[0]}-cell`}
              aria-sort={view.sort === column[0]
                ? view.direction === 'asc'
                  ? 'ascending'
                  : 'descending'
                : 'none'}
              ><button onclick={() => onsort(column[0] as View['sort'])}
                >{column[1]}
                {view.sort === column[0] ? (view.direction === 'asc' ? '↑' : '↓') : ''}</button
              ></th
            >
          {/each}<th class="action-cell"><span class="sr-only">Actions</span></th></tr
        ></thead
      >
      <tbody>
        {#each indices as index, position (entries[index]!.id)}
          {@const entry = entries[index]!}
          {@const gap = index - (position ? indices[position - 1]! + 1 : 0)}
          {@const parts = filenameParts(entry)}
          {#if gap > 0}<tr aria-hidden="true" class="spacer"
              ><td colspan={columnSpan} style:height={`${gap * rowHeight}px`}></td></tr
            >{/if}
          <tr
            class:selected={!!selected[entry.id]}
            data-entry-row={entry.id}
            style:height={`${rowHeight}px`}
            aria-rowindex={index + 2}
            onfocusin={() => (focused = entry.id)}
            onfocusout={() => (focused = '')}
            oncontextmenu={(event) => {
              event.preventDefault();
              ondetails(entry);
            }}
          >
            <td class="check-cell"
              ><label class="check-hit"
                ><input
                  type="checkbox"
                  aria-label={`Select ${entry.name}`}
                  checked={!!selected[entry.id]}
                  onclick={(event) => onselect(entry.id, event.shiftKey)}
                /></label
              ></td
            >
            <td class="name-cell"
              ><button
                class="entry-name"
                data-entry-id={entry.id}
                aria-label={entry.name}
                onclick={(event) => {
                  if (event.ctrlKey || event.metaKey || event.shiftKey)
                    onselect(entry.id, event.shiftKey);
                  else onopen(entry);
                }}
                ><FileMark {entry} /><span class="name-and-meta"
                  ><span class="file-name"><span>{parts[0]}</span><span>{parts[1]}</span></span
                  ><span class="mobile-meta"
                    >{entry.kind === 'directory'
                      ? 'Folder'
                      : `${entry.size != null && !entry.size_exact ? '≈ ' : ''}${formatBytes(entry.size)}`}
                    · {formatDate(entry)}</span
                  ></span
                ></button
              ></td
            >
            <td class="type-cell"
              >{entry.kind === 'directory'
                ? 'Folder'
                : (entry.extension?.toUpperCase() ?? 'File')}</td
            >
            <td class="size-cell"
              >{entry.kind === 'directory'
                ? ''
                : `${entry.size != null && !entry.size_exact ? '≈ ' : ''}${formatBytes(entry.size)}`}</td
            >
            <td class="date-cell" title={formatDate(entry)}>{formatDate(entry)}</td>
            <td class="action-cell"
              ><button
                class="icon-button"
                aria-label={`Details for ${entry.name}`}
                onclick={() => ondetails(entry)}><Icon name="more" size={17} /></button
              ></td
            >
          </tr>
        {/each}
        {#if entries.length > (indices.at(-1) ?? -1) + 1}<tr aria-hidden="true" class="spacer"
            ><td
              colspan={columnSpan}
              style:height={`${(entries.length - (indices.at(-1) ?? -1) - 1) * rowHeight}px`}
            ></td></tr
          >{/if}
      </tbody>
    </table>
  {/if}
  {#if hasMore}<div class="load-more">
      <button class="button secondary" disabled={busy} onclick={onmore}
        >{busy ? 'Loading…' : 'Load more files'}</button
      >
    </div>{/if}
</div>
