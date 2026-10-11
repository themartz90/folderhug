package thumbnails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/upstream"
)

type memorySource struct {
	mu                sync.Mutex
	files             map[string]testfixture.File
	calls             map[string]int
	gate              <-chan struct{}
	started, finished chan struct{}
	active, maxActive int
}

func (f *memorySource) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	target, err := root.Resolve(path)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls[method+" "+target.Path]++
	files := make(map[string]testfixture.File, len(f.files))
	for key, value := range f.files {
		files[key] = value
	}
	gate := f.gate
	started, finished := f.started, f.finished
	if path != "" {
		f.active++
		if f.active > f.maxActive {
			f.maxActive = f.active
		}
	}
	f.mu.Unlock()
	if path != "" {
		if started != nil {
			started <- struct{}{}
		}
		defer func() {
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
			if finished != nil {
				finished <- struct{}{}
			}
		}()
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	if options.Credentials != nil {
		r.SetBasicAuth(options.Credentials.Username, options.Credentials.Password)
	}
	w := httptest.NewRecorder()
	testfixture.Handler(testfixture.Config{Files: files}).ServeHTTP(w, r)
	response := w.Result()
	response.Request = r
	return response, nil
}

func (f *memorySource) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method+" "+path]
}

type thumbnailFixture struct {
	s        *Service
	db       *store.Store
	profiles *profiles.Service
	auth     *auth.Service
	catalog  *catalog.Service
	p        auth.Principal
	profile  profiles.Profile
	entries  map[string]listing.Entry
	source   *memorySource
	clock    atomic.Int64
	cache    string
}

func newThumbnailFixture(t *testing.T) *thumbnailFixture {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db.DB)
	if err := a.Setup(context.Background(), "synthetic thumbnail owner"); err != nil {
		t.Fatal(err)
	}
	raw, _, err := a.Login(context.Background(), "synthetic thumbnail owner")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(context.Background(), raw, "")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	profileService := profiles.New(db.DB, vault)
	profile, err := profileService.Create(context.Background(), profiles.Input{Name: "Images", RootURL: "https://images.example.test/root/"}, p)
	if err != nil {
		t.Fatal(err)
	}
	source := &memorySource{files: map[string]testfixture.File{
		"/root/photo.jpg":  {Data: syntheticJPEG(t, 6), ContentType: "image/jpeg"},
		"/root/second.jpg": {Data: syntheticJPEG(t, 3), ContentType: "image/jpeg"},
		"/root/bad.png":    {Data: []byte("broken PNG"), ContentType: "image/png"},
		"/root/video.mp4":  {Data: []byte("synthetic video"), ContentType: "video/mp4"},
		"/root/page.svg":   {Data: []byte("<svg>active markup</svg>"), ContentType: "image/svg+xml"},
	}, calls: map[string]int{}}
	catalogService := catalog.New(db.DB, profileService, source)
	page, err := catalogService.Browse(context.Background(), profile.ID, profiles.LeaseID(p), "root", catalog.Query{})
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	s, err := New(db.DB, profileService, a, catalogService, source, nil, Config{CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	f := &thumbnailFixture{s: s, db: db, profiles: profileService, auth: a, catalog: catalogService, p: p, profile: profile, entries: map[string]listing.Entry{}, source: source, cache: cache}
	f.clock.Store(time.Now().Unix())
	s.now = func() time.Time { return time.Unix(f.clock.Load(), 0) }
	for _, entry := range page.Entries {
		f.entries[entry.Name] = entry
	}
	t.Cleanup(func() { f.s.Close(); profileService.Close(); db.Close() })
	return f
}

func (f *thumbnailFixture) request(t *testing.T, name string) Thumbnail {
	t.Helper()
	items, err := f.s.Request(context.Background(), f.p, f.profile.ID, []string{f.entries[name].ID})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	return items[0]
}

func settled(t *testing.T, s *Service, p auth.Principal, id string) (Thumbnail, *File) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		item, file, err := s.Get(context.Background(), p, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.State != "pending" {
			return item, file
		}
		select {
		case <-deadline.C:
			t.Fatal("thumbnail did not finish")
		case <-tick.C:
		}
	}
}

func TestThumbnailCacheHitValidationAndJPEGOutput(t *testing.T) {
	f := newThumbnailFixture(t)
	item := f.request(t, "photo.jpg")
	if item.State != "pending" {
		t.Fatal(item)
	}
	ready, file := settled(t, f.s, f.p, item.ID)
	if ready.State != "ready" || file == nil {
		t.Fatal(ready)
	}
	body, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	image, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil || image.Bounds().Dx() != 40 || image.Bounds().Dy() != 80 || bytes.Contains(body, []byte("Exif")) {
		t.Fatal("invalid oriented metadata-free JPEG", err)
	}
	cached := f.request(t, "photo.jpg")
	if cached.ID != item.ID || cached.State != "ready" || f.source.count("GET", "/root/photo.jpg") != 1 {
		t.Fatal("cache hit refetched source", cached)
	}
	f.clock.Add(61)
	pending := f.request(t, "photo.jpg")
	if pending.State != "pending" {
		t.Fatal("stale validator was not rechecked")
	}
	ready, file = settled(t, f.s, f.p, item.ID)
	if ready.State != "ready" {
		t.Fatal(ready)
	}
	file.Close()
	if f.source.count("HEAD", "/root/photo.jpg") != 1 || f.source.count("GET", "/root/photo.jpg") != 1 {
		t.Fatal("unchanged validator refetched image")
	}
	f.source.mu.Lock()
	f.source.files["/root/photo.jpg"] = testfixture.File{Data: syntheticJPEG(t, 1), ContentType: "image/jpeg"}
	f.source.mu.Unlock()
	f.clock.Add(61)
	f.request(t, "photo.jpg")
	ready, file = settled(t, f.s, f.p, item.ID)
	if ready.State != "ready" {
		t.Fatal(ready)
	}
	image, err = jpeg.Decode(file)
	file.Close()
	if err != nil || image.Bounds().Dx() != 80 || image.Bounds().Dy() != 40 || f.source.count("GET", "/root/photo.jpg") != 2 {
		t.Fatal("changed validator did not invalidate image", err)
	}
}

func TestNegativeCacheUnsupportedVideoAndDisabledPreviews(t *testing.T) {
	f := newThumbnailFixture(t)
	for _, name := range []string{"bad.png", "page.svg", "video.mp4"} {
		item := f.request(t, name)
		result, file := settled(t, f.s, f.p, item.ID)
		if result.State != "unavailable" || file != nil {
			t.Fatal(name, result)
		}
		again := f.request(t, name)
		if again.ID != item.ID || again.State != "unavailable" {
			t.Fatal("negative result not cached")
		}
	}
	if f.source.count("GET", "/root/bad.png") != 1 || f.source.count("GET", "/root/video.mp4") != 0 {
		t.Fatal("negative/disabled video cache refetched")
	}
	f.clock.Add(301)
	item := f.request(t, "bad.png")
	settled(t, f.s, f.p, item.ID)
	if f.source.count("GET", "/root/bad.png") != 2 {
		t.Fatal("negative TTL never retried")
	}
	if _, err := f.profiles.Patch(context.Background(), f.profile.ID, map[string]json.RawMessage{"previews_enabled": json.RawMessage(`false`)}, f.p); err != nil {
		t.Fatal(err)
	}
	item = f.request(t, "photo.jpg")
	if item.State != "disabled" || f.source.count("GET", "/root/photo.jpg") != 0 {
		t.Fatal("disabled preview fetched source", item)
	}
}

func TestImageOnlyBatchDoesNotFetchVideo(t *testing.T) {
	f := newThumbnailFixture(t)
	items, err := f.s.Request(context.Background(), f.p, f.profile.ID, []string{f.entries["video.mp4"].ID, f.entries["photo.jpg"].ID})
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	if items[0].State != "unavailable" || items[1].State != "pending" {
		t.Fatal("only images should enter the thumbnail queue", items)
	}
	ready, file := settled(t, f.s, f.p, items[1].ID)
	if ready.State != "ready" || file == nil {
		t.Fatal("image thumbnail did not finish", ready)
	}
	_, err = jpeg.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal("image thumbnail is not a JPEG", err)
	}
	f.clock.Add(int64(negativeTTL.Seconds()) + 1)
	video := f.request(t, "video.mp4")
	if video.State != "unavailable" {
		t.Fatal("expired video result entered the queue", video)
	}
	result, file := settled(t, f.s, f.p, video.ID)
	if result.State != "unavailable" || file != nil {
		t.Fatal("video thumbnail returned content", result)
	}
	if f.source.count("GET", "/root/video.mp4") != 0 || f.source.count("HEAD", "/root/video.mp4") != 0 {
		t.Fatal("video thumbnail request fetched upstream metadata or content")
	}
}

func TestThumbnailPermissionsCredentialRevisionAndTemporaryLease(t *testing.T) {
	f := newThumbnailFixture(t)
	ctx := context.Background()
	item := f.request(t, "photo.jpg")
	_, file := settled(t, f.s, f.p, item.ID)
	file.Close()
	for _, input := range []auth.TokenCreate{{Name: "wrong profile", ProfileIDs: []string{"other"}, Scopes: []string{"content:read"}}, {Name: "wrong scope", ProfileIDs: []string{f.profile.ID}, Scopes: []string{"browse:read"}}} {
		_, raw, err := f.auth.CreateToken(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.auth.Authenticate(ctx, "", raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.s.Get(ctx, p, item.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign token read thumbnail", err)
		}
	}
	patch := map[string]json.RawMessage{"auth_type": json.RawMessage(`"basic"`), "credentials": json.RawMessage(`{"username":"private-image-user","password":"private-image-password","remember":true}`)}
	if _, err := f.profiles.Patch(ctx, f.profile.ID, patch, f.p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Get(ctx, f.p, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old credential cache survived revision", err)
	}
	changed := f.request(t, "photo.jpg")
	if changed.ID == item.ID {
		t.Fatal("new credentials reused old thumbnail identity")
	}
	_, file = settled(t, f.s, f.p, changed.ID)
	file.Close()
	var key string
	if err := f.db.DB.QueryRow(`SELECT cache_key FROM thumbnails WHERE id=?`, changed.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(key, "private-image") || len(key) != 64 {
		t.Fatal("credential fingerprint not opaque HMAC")
	}
	if _, err := f.profiles.Patch(ctx, f.profile.ID, map[string]json.RawMessage{"credentials": json.RawMessage(`{"username":"temporary-user","password":"temporary-password","remember":false}`)}, f.p); err != nil {
		t.Fatal(err)
	}
	temporary := f.request(t, "photo.jpg")
	_, file = settled(t, f.s, f.p, temporary.ID)
	file.Close()
	f.profiles.ClearPrincipal(profiles.LeaseID(f.p))
	if _, _, err := f.s.Get(ctx, f.p, temporary.ID); !errors.Is(err, profiles.ErrLocked) {
		t.Fatal("expired lease retained cached access", err)
	}
}

func TestEvictionPinsOpenFilesAndClearPreservesReader(t *testing.T) {
	f := newThumbnailFixture(t)
	one := f.request(t, "photo.jpg")
	_, open := settled(t, f.s, f.p, one.ID)
	f.s.mu.Lock()
	firstSize := f.s.bytes
	f.s.maxBytes = firstSize + 20
	f.s.mu.Unlock()
	two := f.request(t, "second.jpg")
	state, file := settled(t, f.s, f.p, two.ID)
	if file != nil {
		file.Close()
	}
	if state.State != "unavailable" {
		t.Fatal("cache exceeded byte budget while first file pinned", state)
	}
	if _, err := jpeg.Decode(open); err != nil {
		t.Fatal("eviction broke active reader", err)
	}
	open.Close()
	f.s.mu.Lock()
	if f.s.bytes != 0 {
		t.Fatal("retired file remained after reader close", f.s.bytes)
	}
	f.s.maxBytes = 1 << 30
	f.s.mu.Unlock()
	f.clock.Add(301)
	two = f.request(t, "second.jpg")
	_, open = settled(t, f.s, f.p, two.ID)
	if err := f.s.ClearProfile(context.Background(), f.profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Get(context.Background(), f.p, two.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cleared thumbnail still available", err)
	}
	if _, err := jpeg.Decode(open); err != nil {
		t.Fatal("cache clear broke active reader", err)
	}
	open.Close()
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.bytes != 0 {
		t.Fatal("cache clear leaked bytes", f.s.bytes)
	}
}

func TestRestartClearsOnlyManagedCacheFiles(t *testing.T) {
	f := newThumbnailFixture(t)
	item := f.request(t, "photo.jpg")
	_, file := settled(t, f.s, f.p, item.ID)
	file.Close()
	note := filepath.Join(f.cache, "thumbnails", "keep-notes.txt")
	if err := os.WriteFile(note, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(f.cache, "thumbnails", strings.Repeat("a", 30)+".jpg")
	if err := os.WriteFile(unmanaged, []byte("unmanaged image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(f.db.DB, f.profiles, f.auth, f.catalog, f.source, nil, Config{CacheDir: f.cache})
	if err != nil {
		t.Fatal(err)
	}
	f.s = restarted
	if _, _, err := restarted.Get(context.Background(), f.p, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old process cache key survived", err)
	}
	if body, err := os.ReadFile(note); err != nil || string(body) != "preserve" {
		t.Fatal("cache cleanup deleted unrelated file", err)
	}
	entries, err := os.ReadDir(filepath.Dir(note))
	if err != nil || len(entries) != 2 {
		t.Fatal("managed orphan cache file not removed", len(entries), err)
	}
}

func TestBoundedImageQueue(t *testing.T) {
	f := newThumbnailFixture(t)
	ctx := context.Background()
	payload := syntheticJPEG(t, 1)
	f.source.mu.Lock()
	for i := 0; i < 101; i++ {
		f.source.files[fmt.Sprintf("/root/queued%03d.jpg", i)] = testfixture.File{Data: payload, ContentType: "image/jpeg"}
	}
	f.source.mu.Unlock()
	if _, err := f.profiles.Patch(ctx, f.profile.ID, map[string]json.RawMessage{"auth_type": json.RawMessage(`"basic"`), "credentials": json.RawMessage(`{"username":"temporary","password":"temporary-secret","remember":false}`)}, f.p); err != nil {
		t.Fatal(err)
	}
	page, err := f.catalog.Browse(ctx, f.profile.ID, profiles.LeaseID(f.p), "root", catalog.Query{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, entry := range page.Entries {
		if strings.HasPrefix(entry.Name, "queued") {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) != 101 {
		t.Fatal("queue fixtures missing", len(ids))
	}
	gate := make(chan struct{})
	f.source.mu.Lock()
	f.source.gate = gate
	f.source.mu.Unlock()
	if _, err := f.s.Request(ctx, f.p, f.profile.ID, ids[:50]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Request(ctx, f.p, f.profile.ID, ids[50:100]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Request(ctx, f.p, f.profile.ID, ids[100:]); !errors.Is(err, ErrBusy) {
		t.Fatal("queue admitted more than100 jobs", err)
	}
	if _, err := f.s.Request(ctx, f.p, f.profile.ID, ids[:51]); !errors.Is(err, ErrInvalid) {
		t.Fatal("batch admitted more than50 entries", err)
	}
	if _, err := f.s.Request(ctx, f.p, f.profile.ID, []string{ids[0], ids[0]}); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate batch IDs accepted", err)
	}
	f.source.mu.Lock()
	defer f.source.mu.Unlock()
	if f.source.maxActive > 2 {
		t.Fatal("image queue concurrency escaped", f.source.maxActive)
	}
}

func TestImageQueueCredentialCancellation(t *testing.T) {
	f := newThumbnailFixture(t)
	ctx := context.Background()
	if _, err := f.profiles.Patch(ctx, f.profile.ID, map[string]json.RawMessage{"auth_type": json.RawMessage(`"basic"`), "credentials": json.RawMessage(`{"username":"temporary","password":"temporary-secret","remember":false}`)}, f.p); err != nil {
		t.Fatal(err)
	}
	page, err := f.catalog.Browse(ctx, f.profile.ID, profiles.LeaseID(f.p), "root", catalog.Query{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, entry := range page.Entries {
		if entry.Name == "photo.jpg" || entry.Name == "second.jpg" || entry.Name == "bad.png" {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatal("cancellation fixtures missing", len(ids))
	}
	started, finished := make(chan struct{}, 3), make(chan struct{}, 3)
	f.source.mu.Lock()
	f.source.gate = make(chan struct{})
	f.source.started, f.source.finished = started, finished
	f.source.mu.Unlock()
	items, err := f.s.Request(ctx, f.p, f.profile.ID, ids)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-deadline.C:
			t.Fatal("image workers did not start both upstream fetches")
		}
	}
	f.profiles.ClearPrincipal(profiles.LeaseID(f.p))
	deadline.Reset(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-deadline.C:
			t.Fatal("cancelled credentials left an active upstream fetch running")
		}
	}
	deadline.Reset(3 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		f.s.mu.Lock()
		pending := len(f.s.queued)
		f.s.mu.Unlock()
		if pending == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("cancelled image queue bookkeeping did not settle")
		case <-tick.C:
		}
	}
	for _, item := range items {
		row, err := f.s.byID(ctx, item.ID)
		if err != nil || row.State != "unavailable" {
			t.Fatal("cancelled thumbnail did not become unavailable", row.State, err)
		}
	}
	f.source.mu.Lock()
	defer f.source.mu.Unlock()
	calls := 0
	for key, n := range f.source.calls {
		if strings.HasPrefix(key, "GET /root/") && key != "GET /root/" {
			calls += n
		}
	}
	if f.source.active != 0 || f.source.maxActive != 2 || calls != 2 {
		t.Fatal("image queue concurrency or stale credential boundary escaped", f.source.maxActive, calls)
	}
}

func TestCompressedInputBudget(t *testing.T) {
	f := newThumbnailFixture(t)
	f.source.mu.Lock()
	f.source.files["/root/large.png"] = testfixture.DeterministicFile(maxInput + 1)
	f.source.mu.Unlock()
	page, err := f.catalog.Browse(context.Background(), f.profile.ID, profiles.LeaseID(f.p), "root", catalog.Query{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		f.entries[entry.Name] = entry
	}
	item := f.request(t, "large.png")
	result, file := settled(t, f.s, f.p, item.ID)
	if result.State != "unavailable" || file != nil {
		t.Fatal("oversized compressed image accepted", result)
	}
}
