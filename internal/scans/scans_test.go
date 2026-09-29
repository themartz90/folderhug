package scans

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/upstream"
)

type fixtureSource struct {
	mu      sync.Mutex
	dirs    map[string][]string
	fail    map[string]bool
	block   string
	started chan string
	release chan struct{}
	alias   bool
	calls   map[string]int
}

func (f *fixtureSource) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	f.mu.Lock()
	lookupPath, _ := url.PathUnescape(path)
	names, exists := f.dirs[lookupPath]
	names = append([]string(nil), names...)
	failure := f.fail[path]
	blocked := f.block != "" && path == f.block
	f.calls[path]++
	started := f.started
	release := f.release
	alias := f.alias
	f.mu.Unlock()
	if method != "GET" || options.Interactive || options.MaxBytes != 8<<20 {
		return nil, errors.New("unexpected scanner fetch")
	}
	if started != nil {
		select {
		case started <- path:
		default:
		}
	}
	if blocked {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
	}
	if failure {
		return nil, upstream.ErrNetwork
	}
	if !exists {
		return nil, upstream.ErrNetwork
	}
	var body strings.Builder
	body.WriteString(`<h1>Index of /synthetic/</h1><hr><pre><a href="../">../</a>` + "\n")
	for _, name := range names {
		escaped := url.PathEscape(strings.TrimSuffix(name, "/"))
		if alias {
			escaped = strings.ReplaceAll(escaped, "folder", "%66older")
		}
		if strings.HasSuffix(name, "/") {
			escaped += "/"
		}
		fmt.Fprintf(&body, "<a href=\"%s\">%s</a> 02-Jan-2026 12:30 42\n", escaped, html.EscapeString(name))
	}
	body.WriteString(`</pre><hr>`)
	u, err := root.Resolve(path)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body.String())), Request: &http.Request{URL: u}}, nil
}

type fixture struct {
	s         *Service
	source    *fixtureSource
	profile   profiles.Profile
	owner     auth.Principal
	directory string
}

func setup(t *testing.T, dirs map[string][]string) fixture {
	t.Helper()
	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	p := profiles.New(db.DB, vault)
	a := auth.New(db.DB)
	expires := time.Now().Add(time.Hour)
	_, err = db.DB.Exec("INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at) VALUES('synthetic',X'01','csrf',?)", expires.Unix())
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{Owner: true, CreatorID: "owner", SessionID: "synthetic", ExpiresAt: expires}
	profile, err := p.Create(t.Context(), profiles.Input{Name: "Synthetic", RootURL: "https://files.example.test/synthetic/"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	source := &fixtureSource{dirs: dirs, fail: make(map[string]bool), calls: make(map[string]int), started: make(chan string, 20)}
	c := catalog.New(db.DB, p, source)
	s := New(db.DB, p, a, c, source, nil)
	t.Cleanup(func() { s.Close(); p.Close(); db.Close() })
	return fixture{s, source, profile, owner, directory}
}
func start(t *testing.T, f fixture) {
	t.Helper()
	if err := f.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func create(t *testing.T, f fixture, key string, in Input) Scan {
	t.Helper()
	j, err := f.s.Create(t.Context(), f.owner, f.profile.ID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func wait(t *testing.T, s *Service, id string, states ...string) job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if j.State == state {
				return j
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := s.load(context.Background(), id)
	t.Fatalf("scan remained %s: %+v", j.State, j.Error)
	return j
}
func search(t *testing.T, f fixture, q string) SearchPage {
	t.Helper()
	page, err := f.s.Search(t.Context(), f.owner, SearchQuery{Q: q})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNestedScanFiltersAndNaturalSearch(t *testing.T) {
	f := setup(t, map[string][]string{"": {"nested/", "Episode 10.mp4", "text.txt", "nested/"}, "nested/": {"deep/", "Episode 2.mp4"}, "nested/deep/": {"Episode 1.mp4"}})
	start(t, f)
	j := create(t, f, "videos", Input{Categories: []string{"video"}})
	done := wait(t, f.s, j.ID, "completed")
	if done.DirectoriesVisited != 3 || done.FilesFound != 4 {
		t.Fatalf("progress=%+v", done)
	}
	page := search(t, f, "episode")
	if len(page.Entries) != 3 || page.Entries[0].Name != "Episode 1.mp4" || page.Entries[1].Name != "Episode 2.mp4" {
		t.Fatalf("search=%+v", page)
	}
	if len(search(t, f, "text").Entries) != 0 {
		t.Fatal("category filter did not filter output")
	}
	if page.Coverage[0].State != "partial" {
		t.Fatal("video-only scan claimed full index coverage")
	}
	for _, entry := range page.Entries {
		if _, err := f.s.catalog.Entry(t.Context(), f.profile.ID, profiles.LeaseID(f.owner), entry.ID); err != nil {
			t.Fatal("scan did not make safe catalog identity", err)
		}
	}
	first, err := f.s.Search(t.Context(), f.owner, SearchQuery{Q: "episode", Query: catalog.Query{Limit: 1}})
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	second, err := f.s.Search(t.Context(), f.owner, SearchQuery{Q: "episode", Query: catalog.Query{Limit: 1, Cursor: *first.NextCursor}})
	if err != nil || second.Entries[0].Name != "Episode 2.mp4" {
		t.Fatal(second, err)
	}
	_, err = f.s.Search(t.Context(), f.owner, SearchQuery{Q: "different", Query: catalog.Query{Limit: 1, Cursor: *first.NextCursor}})
	if !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("cursor changed query", err)
	}
	f.source.mu.Lock()
	f.source.dirs["nested/deep/"] = []string{"Episode 3.mp4"}
	f.source.mu.Unlock()
	rescan := create(t, f, "rescan", Input{Categories: []string{"video"}})
	wait(t, f.s, rescan.ID, "completed")
	if _, err = f.s.Search(t.Context(), f.owner, SearchQuery{Q: "episode", Query: catalog.Query{Limit: 1, Cursor: *first.NextCursor}}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("cursor survived index mutation", err)
	}
}

func TestPartialScanPreservesUnvisitedIndexAndCompleteScanPrunes(t *testing.T) {
	f := setup(t, map[string][]string{"": {"nested/", "root.txt"}, "nested/": {"keep.txt", "old.mp4"}})
	start(t, f)
	j := create(t, f, "initial", Input{})
	wait(t, f.s, j.ID, "completed")
	f.source.mu.Lock()
	f.source.dirs["nested/"] = []string{}
	f.source.fail["nested/"] = true
	f.source.mu.Unlock()
	j = create(t, f, "partial", Input{})
	wait(t, f.s, j.ID, "partial")
	if len(search(t, f, "keep.txt").Entries) != 1 {
		t.Fatal("failed folder erased old index")
	}
	f.source.mu.Lock()
	f.source.fail["nested/"] = false
	f.source.mu.Unlock()
	j = create(t, f, "videos", Input{Categories: []string{"video"}})
	wait(t, f.s, j.ID, "completed")
	if len(search(t, f, "old.mp4").Entries) != 0 || len(search(t, f, "keep.txt").Entries) != 1 {
		t.Fatal("filtered rescan pruned outside its category scope")
	}
	j = create(t, f, "complete", Input{})
	wait(t, f.s, j.ID, "completed")
	if len(search(t, f, "keep.txt").Entries) != 0 {
		t.Fatal("complete scan did not prune missing entries")
	}
}

func TestLimitsExclusionsAndIdempotency(t *testing.T) {
	f := setup(t, map[string][]string{"": {"nested/", "one.txt", "two.txt"}, "nested/": {"three.txt"}})
	j := create(t, f, "same", Input{MaxEntries: integer(2)})
	again := create(t, f, "same", Input{MaxEntries: integer(2)})
	if again.ID != j.ID {
		t.Fatal("duplicate job")
	}
	if _, err := f.s.Create(t.Context(), f.owner, f.profile.ID, "same", Input{MaxEntries: integer(3)}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err := f.s.Create(t.Context(), f.owner, f.profile.ID, "other", Input{}); !errors.Is(err, ErrInvalidState) {
		t.Fatal("concurrent profile scan allowed", err)
	}
	start(t, f)
	partial := wait(t, f.s, j.ID, "partial")
	if partial.Error == nil || partial.Error.Code != "budget_exceeded" || partial.EntriesSeen != 0 {
		t.Fatal("partial directory committed", partial)
	}
	if _, err := f.s.Action(t.Context(), f.owner, j.ID, "resume"); !errors.Is(err, ErrInvalidState) {
		t.Fatal("budget-capped job resumed", err)
	}
	j = create(t, f, "depth", Input{MaxDepth: integer(0)})
	partial = wait(t, f.s, j.ID, "partial")
	if partial.DirectoriesSkipped != 1 || partial.DirectoriesVisited != 1 {
		t.Fatal(partial)
	}
	j = create(t, f, "exclude", Input{ExcludedDirectories: []string{"nested"}})
	partial = wait(t, f.s, j.ID, "partial")
	if partial.DirectoriesSkipped != 1 {
		t.Fatal(partial)
	}
}

func TestPauseRestartResumeAndGenerationChange(t *testing.T) {
	f := setup(t, map[string][]string{"": {"nested/", "one.txt"}, "nested/": {"two.txt"}})
	f.source.block = "nested/"
	start(t, f)
	j := create(t, f, "restart", Input{})
	for path := range f.source.started {
		if path == "nested/" {
			break
		}
	}
	f.s.Close()
	interrupted := wait(t, f.s, j.ID, "interrupted")
	if interrupted.DirectoriesVisited != 1 {
		t.Fatal("committed frontier lost", interrupted)
	}
	f.source.mu.Lock()
	f.source.block = ""
	f.source.mu.Unlock()
	restarted := New(f.s.db, f.s.profiles, f.s.auth, f.s.catalog, f.source, nil)
	t.Cleanup(restarted.Close)
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Action(t.Context(), f.owner, j.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	done := wait(t, restarted, j.ID, "completed")
	if done.DirectoriesVisited != 2 {
		t.Fatal("visited directory repeated", done)
	}
	f.source.mu.Lock()
	rootCalls := f.source.calls[""]
	f.source.mu.Unlock()
	if rootCalls != 1 {
		t.Fatal("restart repeated root", rootCalls)
	}
	f.source.mu.Lock()
	f.source.block = "nested/"
	f.source.mu.Unlock()
	next, err := restarted.Create(t.Context(), f.owner, f.profile.ID, "changed", Input{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Action(t.Context(), f.owner, next.ID, "pause")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.profiles.Patch(t.Context(), f.profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://files.example.test/other/"`)}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Action(t.Context(), f.owner, next.ID, "resume"); !errors.Is(err, ErrInvalidState) {
		t.Fatal("changed source resumed", err)
	}
}

func TestPermissionsRevocationAndCSV(t *testing.T) {
	f := setup(t, map[string][]string{"": {" =formula.txt", "+formula.txt", "normal.txt", "nested/"}, "nested/": {"inside.txt"}})
	start(t, f)
	j := create(t, f, "export", Input{})
	wait(t, f.s, j.ID, "completed")
	var output bytes.Buffer
	if err := f.s.Export(t.Context(), f.owner, j.ID, "csv", &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "' =formula.txt") || !strings.Contains(output.String(), "'+formula.txt") {
		t.Fatal("CSV formula not neutralized", output.String())
	}
	output.Reset()
	if err := f.s.Export(t.Context(), f.owner, j.ID, "json", &output); err != nil {
		t.Fatal(err)
	}
	var exported map[string]any
	if err := json.Unmarshal(output.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	_, raw, err := f.s.auth.CreateToken(t.Context(), auth.TokenCreate{Name: "reader", ProfileIDs: []string{"other"}, Scopes: []string{"browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.s.auth.Authenticate(t.Context(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Get(t.Context(), foreign, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign scan exposed", err)
	}
	page, err := f.s.Search(t.Context(), foreign, SearchQuery{Q: "txt"})
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("foreign index exposed", page, err)
	}
	token, raw, err := f.s.auth.CreateToken(t.Context(), auth.TokenCreate{Name: "scanner", ProfileIDs: []string{f.profile.ID}, Scopes: []string{"scan:write", "browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.auth.Authenticate(t.Context(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	f.source.mu.Lock()
	f.source.block = "nested/"
	f.source.mu.Unlock()
	tokenJob, err := f.s.Create(t.Context(), p, f.profile.ID, "token-job", Input{})
	if err != nil {
		t.Fatal(err)
	}
	for path := range f.source.started {
		if path == "nested/" {
			break
		}
	}
	if err = f.s.auth.DeleteToken(t.Context(), token.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, f.s, tokenJob.ID, "cancelled")
	if _, err = f.s.Search(t.Context(), p, SearchQuery{Q: "txt"}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked token read index", err)
	}
}

func TestScan100000(t *testing.T) {
	if os.Getenv("HTTP_WRAPPER_SCAN_BENCHMARK") != "1" {
		t.Skip("set HTTP_WRAPPER_SCAN_BENCHMARK=1 for the synthetic 100000-entry measurement")
	}
	dirs := map[string][]string{"": {}}
	for folder := range 4 {
		path := fmt.Sprintf("part%d/", folder)
		dirs[""] = append(dirs[""], path)
		for i := range 24999 {
			dirs[path] = append(dirs[path], fmt.Sprintf("file%05d.txt", folder*24999+i))
		}
	}
	f := setup(t, dirs)
	start(t, f)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	j := create(t, f, "100000", Input{})
	done := wait(t, f.s, j.ID, "completed")
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	if done.EntriesSeen != 100000 || done.FilesFound != 99996 {
		t.Fatal("measurement correctness", done)
	}
	var count, pageCount, pageSize int64
	if err := f.s.db.QueryRow("SELECT count(*) FROM scan_index").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100000 {
		t.Fatal(count)
	}
	f.s.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
	f.s.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	t.Logf("entries=%d files=%d duration=%s heap_before=%d heap_after=%d total_alloc_delta=%d database_logical_bytes=%d", count, done.FilesFound, elapsed, before.HeapAlloc, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc, pageCount*pageSize)
}

func TestOwnerRememberedAndTemporaryCredentialLifetimes(t *testing.T) {
	for _, remember := range []bool{false, true} {
		t.Run(fmt.Sprint(remember), func(t *testing.T) {
			f := setup(t, map[string][]string{"": {"nested/"}, "nested/": {"file.txt"}})
			credentials := fmt.Sprintf(`{"username":"synthetic","password":"synthetic","remember":%t}`, remember)
			if _, err := f.s.profiles.Patch(t.Context(), f.profile.ID, map[string]json.RawMessage{"auth_type": json.RawMessage(`"basic"`), "credentials": json.RawMessage(credentials)}, f.owner); err != nil {
				t.Fatal(err)
			}
			f.source.block = "nested/"
			f.source.release = make(chan struct{})
			start(t, f)
			j := create(t, f, "lifetime", Input{})
			for path := range f.source.started {
				if path == "nested/" {
					break
				}
			}
			f.s.profiles.ClearPrincipal(profiles.LeaseID(f.owner))
			if remember {
				if err := f.s.auth.RevokeSession(t.Context(), f.owner.SessionID); err != nil {
					t.Fatal(err)
				}
				close(f.source.release)
				wait(t, f.s, j.ID, "completed")
			} else {
				wait(t, f.s, j.ID, "waiting_for_credentials")
				again, err := f.s.Create(t.Context(), f.owner, f.profile.ID, "lifetime", Input{})
				if err != nil || again.ID != j.ID {
					t.Fatal("idempotent replay needed cleared upstream credentials", again, err)
				}
			}
		})
	}
}

func TestEncodedScopePruningAndExportReleasesDatabase(t *testing.T) {
	f := setup(t, map[string][]string{"": {"folder/"}, "folder/": {"gone.txt"}})
	f.source.alias = true
	start(t, f)
	j := create(t, f, "encoded", Input{})
	wait(t, f.s, j.ID, "completed")
	page := search(t, f, "folder")
	folderID := ""
	for _, entry := range page.Entries {
		if entry.Kind == "directory" {
			folderID = entry.ID
		}
	}
	if folderID == "" {
		t.Fatal("folder absent")
	}
	// Re-spell the stored path as a listing refresh would, retaining the same ID.
	var encoded string
	if err := f.s.db.QueryRow("SELECT metadata FROM catalog_entries WHERE profile_id=? AND id=?", f.profile.ID, folderID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(encoded), &entry); err != nil {
		t.Fatal(err)
	}
	entry["escaped_path"] = "folder/"
	body, _ := json.Marshal(entry)
	if _, err := f.s.db.Exec("UPDATE catalog_entries SET escaped_path='folder/',metadata=? WHERE profile_id=? AND id=?", string(body), f.profile.ID, folderID); err != nil {
		t.Fatal(err)
	}
	f.source.mu.Lock()
	f.source.alias = false
	f.source.dirs["folder/"] = []string{}
	f.source.mu.Unlock()
	subtree := create(t, f, "subtree", Input{DirectoryID: folderID})
	wait(t, f.s, subtree.ID, "completed")
	if len(search(t, f, "gone").Entries) != 0 {
		t.Fatal("escaped path spelling defeated subtree pruning")
	}
	writer := &databaseWriter{s: f.s}
	if err := f.s.Export(t.Context(), f.owner, j.ID, "json", writer); err != nil {
		t.Fatal(err)
	}
}

type databaseWriter struct{ s *Service }

func (w *databaseWriter) Write(p []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var n int
	err := w.s.db.QueryRowContext(ctx, "SELECT 1").Scan(&n)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func TestCompletionFailureAndWholeJobTimeLimit(t *testing.T) {
	t.Run("completion failure", func(t *testing.T) {
		f := setup(t, map[string][]string{"": {"gone.txt"}})
		start(t, f)
		j := create(t, f, "initial", Input{})
		wait(t, f.s, j.ID, "completed")
		if _, err := f.s.db.Exec("CREATE TRIGGER synthetic_failure BEFORE DELETE ON scan_index BEGIN SELECT RAISE(ABORT,'synthetic'); END"); err != nil {
			t.Fatal(err)
		}
		f.source.mu.Lock()
		f.source.dirs[""] = []string{}
		f.source.mu.Unlock()
		j = create(t, f, "failure", Input{})
		failed := wait(t, f.s, j.ID, "failed")
		if failed.Error == nil || failed.Error.Code != "internal_error" {
			t.Fatal(failed)
		}
		if len(search(t, f, "gone").Entries) != 1 {
			t.Fatal("failed prune changed index")
		}
	})
	t.Run("whole job elapsed", func(t *testing.T) {
		f := setup(t, map[string][]string{"": {"nested/"}, "nested/": {"file.txt"}})
		f.source.block = "nested/"
		start(t, f)
		j := create(t, f, "time", Input{MaxSeconds: integer(1)})
		partial := wait(t, f.s, j.ID, "partial")
		if partial.ElapsedSeconds < 1 || partial.Error == nil || partial.Error.Code != "budget_exceeded" {
			t.Fatal(partial)
		}
		if _, err := f.s.Action(t.Context(), f.owner, j.ID, "resume"); !errors.Is(err, ErrInvalidState) {
			t.Fatal("time budget reset", err)
		}
	})
}
