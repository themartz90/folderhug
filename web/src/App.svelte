<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import {
    api,
    ApiError,
    currentSession,
    errorMessage,
    idPath,
    refreshSession,
    setSession,
    type Entry,
    type Preferences,
    type Profile,
    type Schema,
  } from './lib/api';
  import { defaultView, readRoute, routeHash, type Route } from './lib/explorer';
  import Explorer from './lib/components/Explorer.svelte';
  import Brand from './lib/components/Brand.svelte';
  import ProfileDialog from './lib/components/ProfileDialog.svelte';
  import Appearance from './lib/components/Appearance.svelte';
  import TransferDialog from './lib/components/TransferDialog.svelte';
  import Media from './lib/components/Media.svelte';
  import Downloads from './lib/screens/Downloads.svelte';
  import Search from './lib/screens/Search.svelte';
  import { readFind, type FindRun } from './lib/find';
  import type { Scan } from './lib/api';
  import Tokens from './lib/screens/Tokens.svelte';
  import Dialog from './lib/ui/Dialog.svelte';
  import Icon from './lib/ui/Icon.svelte';
  import { rememberOpener, clearRememberedFocus } from './lib/ui/focus';
  import { readStoredValue, removeStoredValue, writeStoredValue } from './lib/storage';
  let authenticated = $state(false);
  let starting = $state(true);
  let setupRequired = $state(false);
  let startupError = $state('');
  let password = $state('');
  let repeatPassword = $state('');
  let rememberMe = $state(false);
  let signingIn = $state(false);
  let loginError = $state('');
  let sessionMessage = $state('');
  let profiles = $state<Profile[]>([]);
  let bookmarks = $state<Schema['Bookmark'][]>([]);
  let preferences = $state<Preferences>({
    theme: 'system',
    accent: 'amber',
    density: 'comfortable',
    filter_presets: [],
    profile_order: [],
    profile_views: {},
  });
  let status = $state<Schema['Status'] | null>(null);
  let route = $state<Route>(readRoute());
  let find = $state<FindRun | null>(readFind());
  let findScan = $state<Scan | null>(null);
  let findMissing = $state(false);
  let findFolder = $state<Entry | null>(null);
  let findScope = $derived(find ? `${find.profileId}:${find.directoryId}` : '');
  let findRunning = $derived(!!findScan && ['queued', 'running'].includes(findScan.state));
  let canCloseFind = $derived(findMissing || (!!findScan && !findRunning));
  let findProfile = $derived(profiles.find((profile) => profile.id === find?.profileId)?.name ?? 'Removed profile');
  let findLabel = $derived(find?.directoryId === 'root' ? findProfile : (findFolder?.name ?? 'Selected folder'));
  let findPath = $derived(findFolder ? `${findProfile} / ${displayPath(findFolder.escaped_path)}` : findProfile);
  let systemDark = $state(false);
  let online = $state(navigator.onLine);
  let collapsed = $state(false);
  let drawer = $state(false);
  let appearance = $state(false);
  let confirmingLogoutAll = $state(false);
  let loggingOutAll = $state(false);
  let profileForm = $state<{
    mode: 'create' | 'edit' | 'unlock' | 'remove';
    profile?: Profile;
  } | null>(null);
  type TransferRequest = {
    mode: 'browser' | 'server' | 'scan';
    entries: Entry[];
    profileId?: string;
    directoryId?: string;
    defaultConnections?: 1 | 2 | 4;
    initialDirectOnly?: boolean;
    initialTypes?: FindRun['types'];
  };
  let transfer = $state<(TransferRequest & { id: string; session: number }) | null>(null);
  let media = $state<{ entry: Entry; siblings: Entry[] } | null>(null);
  let notice = $state('');
  let revision = $state(0);
  let profileRevision = $state(0);
  let globalError = $state('');
  let eventStream: EventSource | undefined;
  let eventTimer: ReturnType<typeof setTimeout>;
  let noticeTimer: ReturnType<typeof setTimeout>;
  let prefQueue = Promise.resolve();
  let refreshing: Promise<void> | undefined;
  let refreshGeneration = -1;
  let activeProfile = $derived(profiles.find((profile) => profile.id === route.profile));
  let orderedProfiles = $derived(
    [...profiles].sort((a, b) => {
      const ai = preferences.profile_order.indexOf(a.id),
        bi = preferences.profile_order.indexOf(b.id);
      return (ai < 0 ? 10000 + a.position : ai) - (bi < 0 ? 10000 + b.position : bi);
    }),
  );
  let dark = $derived(
    preferences.theme === 'dark' || (preferences.theme === 'system' && systemDark),
  );
  const accents = {
    blue: ['#285ED1', '#8CB3FF'],
    teal: ['#087F7A', '#73D1C6'],
    violet: ['#7651BD', '#C4A5F5'],
    rose: ['#B03E65', '#F3A2BC'],
    amber: ['#95630A', '#EAC279'],
    graphite: ['#536173', '#B4C0D0'],
  };
  const navigation = [
    ['overview', 'home', 'Overview'],
    ['downloads', 'download', 'Server queue'],
  ] as const;
  onMount(() => {
    document.addEventListener('click', rememberOpener, true);
    const query = matchMedia('(prefers-color-scheme: dark)');
    systemDark = query.matches;
    const theme = () => (systemDark = query.matches);
    query.addEventListener('change', theme);
    const pop = () => {
      route = readRoute();
      drawer = false;
      media = null;
      transfer = null;
      profileForm = null;
    };
    window.addEventListener('popstate', pop);
    const connection = () => {
      online = navigator.onLine;
      if (online && authenticated) {
        revision++;
        void api<Schema['Session']>('/auth/session').catch(() => {});
        renewSession();
      }
    };
    window.addEventListener('online', connection);
    window.addEventListener('offline', connection);
    const foreground = () => {
      if (document.visibilityState === 'visible' && authenticated) renewSession();
    };
    document.addEventListener('visibilitychange', foreground);
    const renewal = setInterval(() => {
      if (authenticated && online && document.visibilityState === 'visible') renewSession();
    }, 12 * 60 * 60 * 1000);
    const poll = setInterval(() => {
      if (authenticated && document.visibilityState === 'visible') {
        revision++;
        void api<Schema['Session']>('/auth/session').catch(() => {});
      }
    }, 15000);
    void restore();
    return () => {
      document.removeEventListener('click', rememberOpener, true);
      eventStream?.close();
      clearInterval(poll);
      clearInterval(renewal);
      clearTimeout(eventTimer);
      clearTimeout(noticeTimer);
      query.removeEventListener('change', theme);
      window.removeEventListener('popstate', pop);
      window.removeEventListener('online', connection);
      window.removeEventListener('offline', connection);
      document.removeEventListener('visibilitychange', foreground);
      setSession(null);
    };
  });
  $effect(() => {
    const id = find?.id;
    void revision;
    if (!authenticated || !id) return;
    void api<Scan>(`/scans/${idPath(id)}`).then((scan) => {
      if (find?.id === id) { findScan = scan; findMissing = false; }
    }).catch((error) => {
      if (find?.id === id && error instanceof ApiError && error.status === 404) { findScan = null; findMissing = true; }
    });
  });
  $effect(() => {
    void findScope;
    if (!authenticated) return;
    const run = untrack(() => find);
    findFolder = null;
    if (!run || run.directoryId === 'root') return;
    const controller = new AbortController();
    void api<Entry>(`/profiles/${idPath(run.profileId)}/entries/${idPath(run.directoryId)}`, { signal: controller.signal })
      .then((entry) => { if (!controller.signal.aborted) findFolder = entry; })
      .catch(() => {});
    return () => controller.abort();
  });
  function displayPath(value: string) {
    try { return decodeURIComponent(value).replace(/^\/+|\/+$/g, ''); }
    catch { return value; }
  }
  function closeFind() {
    if (!find || !canCloseFind) return;
    if (route.page === 'search') browse(find.profileId, find.directoryId);
    find = null;
    findScan = null;
    findMissing = false;
    try { removeStoredValue(sessionStorage, 'find'); } catch { /* Storage is optional. */ }
  }
  function tell(message: string) {
    notice = message;
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => (notice = ''), 6000);
  }
  function clearSession(expired = false) {
    clearRememberedFocus();
    authenticated = false;
    setSession(null);
    eventStream?.close();
    profiles = [];
    bookmarks = [];
    status = null;
    find = null;
    findScan = null;
    try { removeStoredValue(sessionStorage, 'find'); } catch { /* Storage is optional. */ }
    preferences = {
      ...preferences,
      profile_views: {},
      filter_presets: [],
      profile_order: [],
    };
    password = '';
    repeatPassword = '';
    rememberMe = false;
    profileForm = null;
    media = null;
    transfer = null;
    drawer = false;
    appearance = false;
    confirmingLogoutAll = false;
    globalError = '';
    notice = '';
    if (expired) sessionMessage = 'Your session expired. Sign in to continue.';
  }
  async function restore() {
    try {
      const setup = await api<{ setup_required: boolean }>('/auth/setup');
      setupRequired = setup.setup_required;
      if (setupRequired) return;
      const session = await api<Schema['Session']>('/auth/session');
      await begin(session);
    } catch (e) {
      authenticated = false;
      if (!(e instanceof ApiError && e.status === 401)) startupError = errorMessage(e);
    } finally {
      starting = false;
    }
  }
  function renewSession() {
    const generation = currentSession();
    if (refreshing && refreshGeneration === generation) return;
    refreshGeneration = generation;
    const work = refreshSession().catch(() => {}).finally(() => {
      if (refreshing === work) refreshing = undefined;
    });
    refreshing = work;
  }
  async function begin(session: Schema['Session']) {
    setSession(session, () => clearSession(true));
    authenticated = true;
    setupRequired = false;
    sessionMessage = '';
    renewSession();
    await bootstrap();
    subscribe();
  }
  async function signIn(event: SubmitEvent) {
    event.preventDefault();
    signingIn = true;
    loginError = '';
    try {
      if (setupRequired) {
        if (Array.from(password).length < 6) {
          loginError = 'Use at least 6 characters for the owner password.';
          return;
        }
        if (password !== repeatPassword) {
          loginError = 'The passwords do not match.';
          return;
        }
      }
      const session = await api<Schema['Session']>(setupRequired ? '/auth/setup' : '/auth/login', {
        method: 'POST',
        body: { password, remember_me: rememberMe },
      });
      password = '';
      repeatPassword = '';
      await begin(session);
    } catch (e) {
      password = '';
      repeatPassword = '';
      if (setupRequired && e instanceof ApiError && e.status === 409) {
        setupRequired = false;
        sessionMessage = 'Folderhug has already been set up. Sign in to continue.';
        loginError = '';
      } else {
        loginError = errorMessage(e);
      }
    } finally {
      signingIn = false;
    }
  }
  async function bootstrap() {
    globalError = '';
    try {
      const [p, prefs, b, s] = await Promise.all([
        api<{ profiles: Profile[] }>('/profiles'),
        api<Preferences>('/preferences'),
        api<{ bookmarks: Schema['Bookmark'][] }>('/bookmarks'),
        api<Schema['Status']>('/status'),
      ]);
      if (!authenticated) return;
      profiles = p.profiles;
      preferences = prefs;
      bookmarks = b.bookmarks;
      status = s;
      if (!route.profile && profiles.length) {
        let saved: { profile?: string; directory?: string } = {};
        try {
          saved = JSON.parse(readStoredValue(localStorage, 'location') ?? '{}');
        } catch {
          /* Storage may be disabled. */
        }
        const profile = profiles.find((item) => item.id === saved.profile) ?? profiles[0]!;
        route = {
          ...route,
          profile: profile.id,
          directory: profile.id === saved.profile ? (saved.directory ?? 'root') : 'root',
          view: preferences.profile_views[profile.id] ?? defaultView,
        };
        history.replaceState({}, '', routeHash(route));
      } else if (route.profile && !profiles.some((p) => p.id === route.profile)) {
        route = { ...route, profile: profiles[0]?.id ?? '', directory: 'root' };
        history.replaceState({}, '', routeHash(route));
      }
    } catch (e) {
      if (authenticated) globalError = errorMessage(e);
    }
  }
  function subscribe() {
    eventStream?.close();
    eventStream = new EventSource('/api/v1/events');
    for (const type of ['download', 'scan', 'thumbnail', 'reset'])
      eventStream.addEventListener(type, () => {
        clearTimeout(eventTimer);
        eventTimer = setTimeout(() => revision++, 300);
      });
    eventStream.onopen = () => revision++;
    eventStream.onerror = () => {
      if (authenticated) void api<Schema['Session']>('/auth/session').catch(() => {});
    };
  }
  function navigate(next: Route) {
    history.pushState({ scroll: next.scroll }, '', routeHash(next));
    route = next;
    drawer = false;
    if (next.page === 'files')
      try {
        writeStoredValue(
          localStorage,
          'location',
          JSON.stringify({ profile: next.profile, directory: next.directory }),
        );
      } catch {
        /* Location history is optional. */
      }
  }
  function browse(profile: string, directory = 'root') {
    navigate({
      ...route,
      page: 'files',
      profile,
      directory,
      filters: {},
      view: preferences.profile_views[profile] ?? defaultView,
      scroll: 0,
    });
  }
  function savePreferences(patch: Partial<Preferences>): Promise<void> {
    const generation = currentSession();
    const work = prefQueue
      .catch(() => {})
      .then(async () => {
        if (generation !== currentSession())
          throw new DOMException('Session changed', 'AbortError');
        const value = {
          ...preferences,
          ...patch,
          profile_views: {
            ...preferences.profile_views,
            ...patch.profile_views,
          },
        };
        const saved = await api<Preferences>('/preferences', {
          method: 'PUT',
          body: value,
        });
        if (authenticated) preferences = saved;
      });
    prefQueue = work;
    return work;
  }
  async function changed(profile?: Profile) {
    const created = profileForm?.mode === 'create';
    await bootstrap();
    if (profile && created) browse(profile.id);
    revision++;
    profileRevision++;
  }
  async function pin(profile: string, directory: string, name: string) {
    try {
      const existing = bookmarks.find(
        (item) => item.profile_id === profile && item.directory_id === directory,
      );
      if (existing) {
        await api(`/bookmarks/${idPath(existing.id)}`, { method: 'DELETE' });
        bookmarks = bookmarks.filter((item) => item.id !== existing.id);
        tell('Folder unpinned.');
      } else {
        const item = await api<Schema['Bookmark']>('/bookmarks', {
          method: 'POST',
          body: { profile_id: profile, directory_id: directory, name },
        });
        bookmarks = [...bookmarks, item];
        tell('Folder pinned.');
      }
    } catch (e) {
      tell(errorMessage(e));
    }
  }
  function menu() {
    if (matchMedia('(max-width: 800px)').matches) drawer = true;
    else collapsed = !collapsed;
  }
  function openMedia(entry: Entry, siblings: Entry[] = [entry]) {
    if (entry.kind === 'directory') {
      browse(entry.profile_id, entry.id);
      return;
    }
    media = { entry, siblings };
  }
  function queue(entries: Entry[], mode: 'browser' | 'server') {
    media = null;
    const folder = entries.length === 1 && entries[0]?.kind === 'directory' ? entries[0] : null;
    openTransfer(
      folder
        ? {
            entries: [],
            mode: 'scan',
            profileId: folder.profile_id,
            directoryId: folder.id,
          }
        : {
            entries,
            mode,
            defaultConnections:
              profiles.find((profile) => profile.id === entries[0]?.profile_id)
                ?.connections_per_file ?? 1,
          },
    );
  }
  function openTransfer(request: TransferRequest) {
    transfer = {
      ...request,
      id: crypto.randomUUID(),
      session: currentSession(),
    };
  }
  function startedFind(scan: Scan, directOnly: boolean, types: FindRun['types']) {
    const previous = findScan;
    find = { id: scan.id, profileId: scan.profile_id, directoryId: scan.directory_id, directOnly, types };
    findScan = scan;
    findMissing = false;
    if (previous && previous.id !== scan.id && ['queued', 'running'].includes(previous.state)) {
      void api(`/scans/${idPath(previous.id)}/cancel`, { method: 'POST' }).catch((e) => {
        if (!(e instanceof ApiError && e.code === 'invalid_state')) tell(errorMessage(e));
      });
    }
    try { writeStoredValue(sessionStorage, 'find', JSON.stringify(find)); } catch { /* Storage is optional. */ }
    navigate({ ...route, page: 'search', profile: scan.profile_id, directory: scan.directory_id, scroll: 0 });
    revision++;
  }
  function saveFindFilters(types: FindRun['types'], query: string) {
    if (!find) return;
    find = { ...find, types, query };
    try { writeStoredValue(sessionStorage, 'find', JSON.stringify(find)); } catch { /* Storage is optional. */ }
  }
  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST' });
      clearSession();
    } catch (e) {
      tell(errorMessage(e));
    }
  }
  async function logoutAll() {
    loggingOutAll = true;
    try {
      await api('/auth/logout-all', { method: 'POST' });
      clearSession();
      sessionMessage = 'All devices have been signed out.';
    } catch (e) {
      tell(errorMessage(e));
    } finally {
      loggingOutAll = false;
      confirmingLogoutAll = false;
    }
  }
  async function reorder(id: string, delta: number) {
    const ids = orderedProfiles.map((p) => p.id);
    const index = ids.indexOf(id);
    const next = index + delta;
    if (next < 0 || next >= ids.length) return;
    [ids[index], ids[next]] = [ids[next]!, ids[index]!];
    try {
      await savePreferences({ profile_order: ids });
    } catch (e) {
      tell(errorMessage(e));
    }
  }
</script>

<svelte:head
  ><title>Folderhug</title><meta
    name="description"
    content="Browse HTTP folders, preview media, and manage downloads."
  /></svelte:head
>
<div
  class="design app"
  data-theme={dark ? 'dark' : 'light'}
  data-compact={preferences.density === 'compact'}
  style:--accent={accents[preferences.accent][dark ? 1 : 0]}
>
  {#if starting}<main class="login-page">
      <p role="status">Opening Folderhug…</p>
    </main>
  {:else if !authenticated}<main class="login-page">
      <form class="login-card" onsubmit={signIn}>
        <Brand login />
        <p>{setupRequired ? 'Set an owner password to start using Folderhug.' : 'Sign in to browse your HTTP folders.'}</p>
        {#if startupError}<p class="error-banner" role="alert">{startupError}</p>
          <button class="button secondary" type="button" onclick={() => {
            startupError = '';
            starting = true;
            void restore();
          }}>Try again</button>{/if}
        {#if sessionMessage}<p role="status">
            {sessionMessage}
          </p>{/if}{#if loginError}<p class="error-banner" role="alert">
            {loginError}
          </p>{/if}<label class="form-field"
        >Owner password<input
            type="password"
            autocomplete={setupRequired ? 'new-password' : 'current-password'}
            required
            bind:value={password}
          /></label
        >{#if setupRequired}<label class="form-field">Repeat password<input
            type="password"
            autocomplete="new-password"
            required
            bind:value={repeatPassword}
          /></label>
          <p class="field-help">Use at least 6 characters. Spaces and Unicode characters are allowed.</p>{/if}
        <label class="check-label"><input type="checkbox" bind:checked={rememberMe} />Remember me</label>
        <p class="field-help">{rememberMe ? 'Stay signed in for up to 90 days without activity, and up to 1 year in total.' : 'This session lasts up to 30 days.'}</p>
        <button class="button primary" disabled={signingIn || !!startupError}
          >{signingIn ? (setupRequired ? 'Setting up…' : 'Signing in…') : (setupRequired ? 'Create owner account' : 'Sign in')}</button
        >
        {#if !setupRequired}<p class="field-help">Recover the owner password with the server's recovery command.</p>{/if}
      </form>
    </main>
  {:else}
    {#snippet sidebar()}
      <div class="brand">
        <Brand />
      </div>
      <nav class="primary-nav" aria-label="Main navigation">
        {#each navigation as [page, icon, label] (page)}<button
            class:active={route.page === page}
            aria-current={route.page === page ? 'page' : undefined}
            title={label}
            onclick={() => navigate({ ...route, page, scroll: 0 })}
            ><Icon name={icon} size={19} /><span class="nav-label">{label}</span></button
          >{/each}
      </nav>
      <div class="nav-section-title">
        <span class="nav-label">Profiles</span><button
          class="icon-button"
          aria-label="Add profile"
          onclick={() => {
            drawer = false;
            profileForm = { mode: 'create' };
          }}><Icon name="plus" size={16} /></button
        >
      </div>
      <nav class="profile-nav" aria-label="Profiles">
        {#each orderedProfiles as profile (profile.id)}<button
            class:current-profile={route.page === 'files' && route.profile === profile.id}
            aria-current={route.page === 'files' && route.profile === profile.id ? 'page' : undefined}
            title={profile.name}
            onclick={() => browse(profile.id)}
            ><Icon
              name={profile.credential_state === 'locked' ? 'lock' : 'server'}
              size={18}
            /><span class="nav-label truncate">{profile.name}</span
            >{#if route.page === 'files' && route.profile === profile.id}<span class="profile-dot"
              ></span>{/if}</button
          >{/each}
      </nav>
      {#if bookmarks.length}<div class="nav-section-title pin-heading">Pinned folders</div>
        <nav class="pinned-nav" aria-label="Pinned folders">
          {#each bookmarks as bookmark (bookmark.id)}<button
              title={`${profiles.find((p) => p.id === bookmark.profile_id)?.name ?? ''} / ${bookmark.name}`}
              onclick={() => browse(bookmark.profile_id, bookmark.directory_id)}
              ><Icon name="folder" size={18} /><span class="nav-label truncate"
                >{bookmark.name}</span
              ></button
            >{/each}
        </nav>{/if}
      <div class="sidebar-bottom">
        <button
          class="appearance-link"
          title="Appearance"
          onclick={() => {
            drawer = false;
            appearance = true;
          }}
          ><Icon name={dark ? 'moon' : 'sun'} size={19} /><span class="nav-label">Appearance</span
          ><span class="accent-dot"></span></button
        ><button
          class="appearance-link"
          title="Settings"
          onclick={() => navigate({ ...route, page: 'settings', scroll: 0 })}
          ><Icon name="settings" size={19} /><span class="nav-label">Settings</span></button
        ><button class="appearance-link" title="Sign out" onclick={() => void logout()}
          ><Icon name="external" size={19} /><span class="nav-label">Sign out</span></button
        >
      </div>
    {/snippet}
    <div class="app-shell" class:collapsed>
      <aside class="sidebar">{@render sidebar()}</aside>
      <main class="workspace">
        {#if find}<div class="find-status">
          <Icon name="search" size={17} />
          {#if route.page === 'search'}<span class="find-status-label">{findRunning ? 'Finding files in' : 'Results:'} {findLabel}</span>
          {:else}<button class="find-return" onclick={() => navigate({ ...route, page: 'search', profile: find!.profileId, directory: find!.directoryId, scroll: 0 })}>{findRunning ? `Finding files in ${findLabel}…` : `Return to results: ${findLabel}`}</button>{/if}
          {#if canCloseFind}<button class="icon-button" aria-label="Close results" title="Close results" onclick={closeFind}><Icon name="x" size={17} /></button>{/if}
        </div>{/if}
        {#if !online}<p class="warning-banner" role="status">
            You are offline. Reconnect to refresh folders and transfer status.
          </p>{/if}
        {#if globalError}<div class="error-banner" role="alert">
            {globalError}<button class="button secondary" onclick={() => void bootstrap()}
              >Retry loading profiles</button
            >
          </div>{/if}
        {#if route.page === 'files' && activeProfile}<Explorer
            profile={activeProfile}
            {profileRevision}
            {route}
            {preferences}
            {bookmarks}
            onroute={navigate}
            onsavepreferences={savePreferences}
            onprofile={(mode) => (profileForm = { mode, profile: activeProfile! })}
            onscan={(profileId, directoryId) =>
              openTransfer({
                mode: 'scan',
                entries: [],
                profileId,
                directoryId,
              })}
            ontransfer={queue}
            onopen={openMedia}
            onpin={(profile, directory, name) => void pin(profile, directory, name)}
            onmenu={menu}
            onnotice={tell}
          />
        {:else}
          <div class="location-bar">
            <button class="icon-button" aria-label="Toggle sidebar" onclick={menu}
              ><Icon name="menu" /></button
            ><span class="location-section"
              ><Icon
                name={route.page === 'settings'
                  ? 'settings'
                  : (navigation.find((item) => item[0] === route.page)?.[1] ?? 'folder')}
                size={17}
              />{route.page === 'settings'
                ? 'Settings'
                : route.page === 'search' ? findPath : (navigation.find((item) => item[0] === route.page)?.[2] ?? 'Profiles')}</span
            >
          </div>
          {#if route.page === 'downloads'}<Downloads {revision} onnotice={tell} />
          {:else if route.page === 'search'}<Search
              {profiles}
              {find}
              sourcePath={findPath}
              scan={findScan}
              {revision}
              onrefresh={() => revision++}
              onfilters={saveFindFilters}
              onscan={(profileId, directoryId) =>
                openTransfer({
                  mode: 'scan',
                  entries: [],
                  profileId,
                  directoryId,
                  initialDirectOnly: find?.directOnly ?? false,
                  initialTypes: find?.types ?? [],
                })}
              onopen={(entry) => {
                browse(entry.profile_id, entry.parent_id);
                openMedia(entry);
              }}
              onbrowse={browse}
              onback={() => browse(find?.profileId ?? route.profile, find?.directoryId ?? route.directory)}
              onsave={(entries) => queue(entries, 'server')}
              onnotice={tell}
            />
          {:else if route.page === 'settings'}<div class="page-scroll settings-page">
              <div class="page-heading"><h1>Settings</h1></div>
              <section>
                <h2>Profiles</h2>
                {#each orderedProfiles as profile, index (profile.id)}<div class="settings-profile">
                    <span class="truncate">{profile.name}</span><button
                      class="icon-button"
                      aria-label={`Move ${profile.name} up`}
                      disabled={index === 0}
                      onclick={() => void reorder(profile.id, -1)}
                      ><Icon name="arrow-up" size={16} /></button
                    ><button
                      class="icon-button"
                      aria-label={`Move ${profile.name} down`}
                      disabled={index === orderedProfiles.length - 1}
                      onclick={() => void reorder(profile.id, 1)}
                      ><Icon name="chevron-down" size={16} /></button
                    ><button
                      class="button secondary"
                      onclick={() => (profileForm = { mode: 'edit', profile })}>Edit profile</button
                    >
                  </div>{/each}<button
                  class="button secondary"
                  onclick={() => (profileForm = { mode: 'create' })}>Add profile</button
                >
              </section>
              <section>
                <h2>Media and downloads</h2>
                <p>
                  Image thumbnails: {status?.image_thumbnails ? 'available' : 'unavailable'}. Video
                  thumbnails: {status?.video_worker ?? 'unknown'}.
                </p>
                <p>
                  Server downloads: {status?.server_downloads ? 'available' : 'unavailable'}. The
                  optional video worker provides posters; playback depends on your browser.
                </p>
                <button class="button secondary" onclick={() => (appearance = true)}
                  >Appearance</button
                >
              </section>
              <section>
                <h2>Account</h2>
                <p>Sign out this browser, or end every owner session.</p>
                <button class="button secondary" onclick={() => void logout()}>Sign out</button>
                <button class="button danger" onclick={() => (confirmingLogoutAll = true)}>Sign out all devices</button>
              </section>
              <Tokens {profiles} onnotice={tell} />
            </div>
          {:else}<div class="page-scroll">
              <div class="page-heading">
                <div>
                  <h1>
                    {profiles.length ? 'Your folders' : 'Add your first profile'}
                  </h1>
                  <p>
                    {profiles.length
                      ? 'Choose an HTTP folder to browse.'
                      : 'Connect a directory listing URL to start browsing.'}
                  </p>
                </div>
                <button class="button primary" onclick={() => (profileForm = { mode: 'create' })}
                  >Add profile</button
                >
              </div>
              {#if !profiles.length}<img class="welcome-mascot" src="/brand/folderhug-mark.svg" alt="" width="120" height="120" />{/if}
              <div class="profile-cards">
                {#each orderedProfiles as profile (profile.id)}<article class="profile-card">
                    <button class="profile-card-main" onclick={() => browse(profile.id)}>
                      <span class="profile-card-icon"
                        ><Icon
                          name={profile.credential_state === 'locked' ? 'lock' : 'server'}
                          size={23}
                        /></span
                      >
                      <h2>{profile.name}</h2>
                      <p>
                        {profile.credential_state === 'locked'
                          ? 'Password needed'
                          : profile.credential_state === 'temporary'
                            ? 'Temporary password'
                            : 'Browse files'}
                      </p>
                    </button>
                    <div class="profile-card-actions">
                      <button onclick={() => (profileForm = { mode: 'edit', profile })}
                        ><Icon name="edit" size={16} />Edit</button
                      >
                      <button class="remove-action" onclick={() => (profileForm = { mode: 'remove', profile })}
                        ><Icon name="trash" size={16} />Remove</button
                      >
                    </div>
                  </article>{/each}
              </div>
              {#if bookmarks.length}<div class="section-heading">
                  <h2>Pinned folders</h2>
                </div>
                <div class="pinned-cards">
                  {#each bookmarks as item (item.id)}<button
                      onclick={() => browse(item.profile_id, item.directory_id)}
                      ><Icon name="folder" /><span
                        ><strong>{item.name}</strong><small
                          >{profiles.find((p) => p.id === item.profile_id)?.name}</small
                        ></span
                      ></button
                    >{/each}
                </div>{/if}
            </div>{/if}
        {/if}
      </main>
    </div>
    {#if drawer}<Dialog title="Profiles and navigation" onclose={() => (drawer = false)}
        >{@render sidebar()}</Dialog
      >{/if}
    {#if appearance}<Appearance
        {preferences}
        onsave={savePreferences}
        onclose={() => (appearance = false)}
      />{/if}
    {#if confirmingLogoutAll}<Dialog title="Sign out all devices" onclose={() => (confirmingLogoutAll = false)}>
        <p class="modal-intro">This ends every owner session, including this one. You will need to sign in again on each device.</p>
        <div class="modal-actions">
          <button class="button secondary" disabled={loggingOutAll} onclick={() => (confirmingLogoutAll = false)}>Cancel</button>
          <button class="button danger" disabled={loggingOutAll} onclick={() => void logoutAll()}>{loggingOutAll ? 'Signing out…' : 'Sign out all devices'}</button>
        </div>
      </Dialog>{/if}
    {#if profileForm}{#key `${profileForm.mode}:${profileForm.profile?.id ?? ''}`}<ProfileDialog
          {...profileForm}
          onclose={() => (profileForm = null)}
          onchanged={(profile) => void changed(profile)}
          onnotice={tell}
        />{/key}{/if}
    {#if transfer}{@const operation = transfer}{#key operation.id}<TransferDialog
          {...operation}
          {preferences}
          onsavepreferences={savePreferences}
          onclose={() => {
            if (transfer?.id === operation.id) transfer = null;
          }}
          ondone={(message) => {
            if (currentSession() !== operation.session) return;
            tell(message);
            revision++;
          }}
          onscanstarted={(scan, directOnly, types) => {
            if (currentSession() !== operation.session) return;
            startedFind(scan, directOnly, types);
          }}
        />{/key}{/if}
    {#if media}{#key media.entry.id}<Media
          {...media}
          previews={profiles.find((profile) => profile.id === media!.entry.profile_id)
            ?.previews_enabled ?? false}
          onclose={() => (media = null)}
          onchange={(entry) => {
            if (media) media = { ...media, entry };
          }}
          onsave={(entries) => queue(entries, 'server')}
          onnotice={tell}
        />{/key}{/if}
    <div class="toast" class:visible={!!notice} role="status">{notice}</div>
  {/if}
</div>
