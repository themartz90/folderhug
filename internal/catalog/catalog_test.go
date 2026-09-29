package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/upstream"
)

type fakeFetcher struct {
	mu       sync.Mutex
	body     string
	err      error
	status   int
	calls    int
	redirect string
	block    bool
	started  chan struct{}
}

func (f *fakeFetcher) Do(ctx context.Context, root *upstream.Root, method, relative string, options upstream.Options) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	body, err, status, redirect, block := f.body, f.err, f.status, f.redirect, f.block
	started := f.started
	f.mu.Unlock()
	if method != http.MethodGet || options.MaxBytes != 8<<20 || options.Timeout != 30*time.Second || !options.Interactive {
		return nil, errors.New("wrong checked fetch options")
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	u, err := root.Resolve(relative)
	if err != nil {
		return nil, err
	}
	if redirect != "" {
		u, err = root.Child(u, redirect)
		if err != nil {
			return nil, err
		}
	}
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{URL: u}}, nil
}

func fixture(t *testing.T) (*Service, *fakeFetcher, profiles.Profile, auth.Principal) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	p := profiles.New(db.DB, vault)
	owner := auth.Principal{Owner: true, CreatorID: "owner", SessionID: "synthetic", ExpiresAt: time.Now().Add(time.Hour)}
	profile, err := p.Create(context.Background(), profiles.Input{Name: "Synthetic", RootURL: "https://example.test/synthetic/"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../tests/fixtures/apache-table.html")
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{body: string(body)}
	s := New(db.DB, p, fetcher)
	t.Cleanup(func() { p.Close(); db.Close() })
	return s, fetcher, profile, owner
}

func browse(t *testing.T, s *Service, p profiles.Profile, owner auth.Principal, q Query) Page {
	t.Helper()
	page, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", q)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestBrowsePersistsEntriesAndRestartsWithExpiredCursors(t *testing.T) {
	s, f, p, owner := fixture(t)
	page := browse(t, s, p, owner, Query{Limit: 2})
	if len(page.Entries) != 2 || page.TotalEntries != 4 || page.NextCursor == nil || page.Stale || page.ParentID != nil || page.DirectoryID != "root" {
		t.Fatalf("page=%+v", page)
	}
	if page.Entries[0].Kind != "directory" {
		t.Fatal("folders-first default missing")
	}
	second := browse(t, s, p, owner, Query{Limit: 2, Cursor: *page.NextCursor})
	if len(second.Entries) != 2 || second.NextCursor != nil || page.SnapshotID != second.SnapshotID || f.calls != 1 {
		t.Fatalf("second=%+v calls=%d", second, f.calls)
	}
	seen := map[string]bool{}
	for _, entry := range append(page.Entries, second.Entries...) {
		if seen[entry.ID] {
			t.Fatal("duplicate page entry")
		}
		seen[entry.ID] = true
	}
	restarted := New(s.db, s.profiles, f)
	entry, err := restarted.Entry(context.Background(), p.ID, profiles.LeaseID(owner), page.Entries[1].ID)
	if err != nil || entry.Name != page.Entries[1].Name {
		t.Fatalf("durable entry=%+v err=%v", entry, err)
	}
	if _, err = restarted.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Limit: 2, Cursor: *page.NextCursor}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal(err)
	}
}

func TestCursorBindsQueryPrincipalProfileAndSnapshot(t *testing.T) {
	s, _, p, owner := fixture(t)
	page := browse(t, s, p, owner, Query{Limit: 1})
	cursor := *page.NextCursor
	for _, q := range []Query{{Limit: 2, Cursor: cursor}, {Limit: 1, Cursor: cursor, Name: "different"}, {Limit: 1, Cursor: cursor + "tampered"}} {
		if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", q); !errors.Is(err, ErrSnapshotExpired) {
			t.Fatal(err)
		}
	}
	if _, err := s.Browse(context.Background(), p.ID, "token:other", "root", Query{Limit: 1, Cursor: cursor}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal(err)
	}
	other, err := s.profiles.Create(context.Background(), profiles.Input{Name: "Other", RootURL: "https://example.test/other/"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Entry(context.Background(), other.ID, profiles.LeaseID(owner), page.Entries[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-profile entry", err)
	}
	if _, err = s.Browse(context.Background(), other.ID, profiles.LeaseID(owner), "root", Query{Limit: 1, Cursor: cursor}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("cross-profile cursor", err)
	}
	for _, id := range []string{"https://example.test/file", "../secret", "root/child", "' OR 1=1 --", strings.Repeat("a", 43)} {
		if _, err := s.Entry(context.Background(), p.ID, profiles.LeaseID(owner), id); !errors.Is(err, ErrNotFound) {
			t.Fatal("hostile entry", err)
		}
	}
	future := page.FetchedAt.Add(SnapshotTTL)
	s.now = func() time.Time { return future }
	if _, err = s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Limit: 1, Cursor: cursor}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("expired", err)
	}
}

func TestRefreshDoesNotMixSnapshotsOrDeleteKnownEntries(t *testing.T) {
	s, f, p, owner := fixture(t)
	first := browse(t, s, p, owner, Query{Limit: 1})
	f.body = `<h1>Index of /synthetic/</h1><hr><pre><a href="new.txt">new.txt</a> 02-Jan-2026 12:30 42</pre><hr>`
	refreshed := browse(t, s, p, owner, Query{Refresh: true})
	if refreshed.SnapshotID == first.SnapshotID || len(refreshed.Entries) != 1 {
		t.Fatal("refresh retained old snapshot")
	}
	old := browse(t, s, p, owner, Query{Limit: 1, Cursor: *first.NextCursor})
	if old.SnapshotID != first.SnapshotID || old.TotalEntries != 4 {
		t.Fatal("pages mixed snapshots")
	}
	if _, err := s.Entry(context.Background(), p.ID, profiles.LeaseID(owner), first.Entries[0].ID); err != nil {
		t.Fatal("listing refresh erased durable catalog path", err)
	}
	if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Refresh: true, Cursor: *first.NextCursor}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestFreshnessAndStaleFailurePolicy(t *testing.T) {
	s, f, p, owner := fixture(t)
	first := browse(t, s, p, owner, Query{Limit: 1})
	current := first.FetchedAt.Add(FreshTTL - time.Second)
	s.now = func() time.Time { return current }
	f.err = upstream.ErrNetwork
	cached := browse(t, s, p, owner, Query{Limit: 1})
	if cached.Stale || f.calls != 1 {
		t.Fatal("fresh cache unexpectedly fetched")
	}
	current = current.Add(2 * time.Second)
	stale := browse(t, s, p, owner, Query{Limit: 1})
	if !stale.Stale || stale.FetchedAt != first.FetchedAt || stale.SnapshotID != first.SnapshotID {
		t.Fatalf("stale=%+v", stale)
	}
	next := browse(t, s, p, owner, Query{Limit: 1, Cursor: *stale.NextCursor})
	if !next.Stale {
		t.Fatal("stale cursor lost stale label")
	}
	f.err = nil
	for status, want := range map[int]error{401: ErrSourceAuth, 403: ErrSourceAuth, 404: ErrNotFound, 429: ErrBudget} {
		f.status = status
		if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Refresh: true}); !errors.Is(err, want) {
			t.Fatalf("status%d: %v", status, err)
		}
	}
	f.status = 200
	f.body = `<html><h1>Sign in</h1><form><input type="password"></form></html>`
	if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Refresh: true}); !errors.Is(err, listing.ErrUnsupported) {
		t.Fatal("unsupported listing was hidden behind stale data", err)
	}
	f.status = 500
	if page := browse(t, s, p, owner, Query{Refresh: true}); !page.Stale {
		t.Fatal("500 did not label fallback")
	}
	current = first.FetchedAt.Add(SnapshotTTL)
	if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{}); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("expired fallback", err)
	}
}

func TestGenerationAndCredentialChangesDoNotReuseCache(t *testing.T) {
	s, f, p, owner := fixture(t)
	page := browse(t, s, p, owner, Query{})
	rootURL := json.RawMessage(`"https://example.test/replaced/"`)
	changed, err := s.profiles.Patch(context.Background(), p.ID, map[string]json.RawMessage{"root_url": rootURL}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Generation == p.Generation {
		t.Fatal("generation did not change")
	}
	if _, err = s.Entry(context.Background(), p.ID, profiles.LeaseID(owner), page.Entries[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old generation accepted", err)
	}
	browse(t, s, changed, owner, Query{})
	if f.calls != 2 {
		t.Fatal("new generation reused cache")
	}
	p, err = s.profiles.Patch(context.Background(), p.ID, map[string]json.RawMessage{"auth_type": json.RawMessage(`"basic"`), "credentials": json.RawMessage(`{"username":"one","password":"synthetic"}`)}, owner)
	if err != nil {
		t.Fatal(err)
	}
	browse(t, s, p, owner, Query{})
	if err := s.profiles.Unlock(p, profiles.LeaseID(owner), profiles.Credentials{Username: "two", Password: "other"}, owner.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	browse(t, s, p, owner, Query{})
	if f.calls != 4 {
		t.Fatal("changed credentials reused cache")
	}
	s.profiles.ClearPrincipal(profiles.LeaseID(owner))
	if _, err = s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{}); !errors.Is(err, profiles.ErrLocked) {
		t.Fatal("locked profile reused cache", err)
	}
}

func TestFinalResponseURLAndCancellation(t *testing.T) {
	s, f, p, owner := fixture(t)
	_, err := s.profiles.Patch(context.Background(), p.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://example.test/synthetic"`)}, owner)
	if err != nil {
		t.Fatal(err)
	}
	f.redirect = "/synthetic/"
	page := browse(t, s, p, owner, Query{})
	if len(page.Entries) != 4 {
		t.Fatal("parser ignored response URL")
	}
	f.redirect = "/synthetic/nested/"
	if _, err := s.Browse(context.Background(), p.ID, profiles.LeaseID(owner), "root", Query{Refresh: true}); !errors.Is(err, listing.ErrUnsupported) {
		t.Fatal("directory alias accepted", err)
	}
	f.block = true
	f.started = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Browse(ctx, p.ID, profiles.LeaseID(owner), "root", Query{Refresh: true})
		done <- err
	}()
	<-f.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not cancel")
	}
}

func TestLargeListingPagination(t *testing.T) {
	s, f, p, owner := fixture(t)
	var body strings.Builder
	body.WriteString(`<h1>Index of /synthetic/</h1><hr><pre>`)
	for i := 9999; i >= 0; i-- {
		fmt.Fprintf(&body, "<a href=\"Episode%%20%d.txt\">truncated</a> 02-Jan-2026 12:30 %d\n", i, i)
	}
	body.WriteString(`</pre><hr>`)
	f.body = body.String()
	page := browse(t, s, p, owner, Query{Limit: 200})
	if page.TotalEntries != 10000 || page.Entries[2].Name != "Episode 2.txt" {
		t.Fatal("natural sorting failed")
	}
	seen := make(map[string]bool)
	for {
		for _, e := range page.Entries {
			if seen[e.ID] {
				t.Fatal("duplicate entry across pages")
			}
			seen[e.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		page = browse(t, s, p, owner, Query{Limit: 200, Cursor: *page.NextCursor})
	}
	if len(seen) != 10000 || f.calls != 1 {
		t.Fatalf("count=%d fetches=%d", len(seen), f.calls)
	}
}
