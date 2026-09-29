package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/upstream"
)

type fixtureFetcher struct {
	mu           sync.Mutex
	handler      http.Handler
	delay        time.Duration
	withoutETag  bool
	unknown      bool
	changeRanges bool
	authFailure  bool
	calls        int
}

func (f *fixtureFetcher) Do(ctx context.Context, root *upstream.Root, method, relative string, options upstream.Options) (*http.Response, error) {
	f.mu.Lock()
	handler, delay, missing, unknown, changed, badAuth := f.handler, f.delay, f.withoutETag, f.unknown, f.changeRanges, f.authFailure
	f.calls++
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := root.Resolve(relative)
	if err != nil {
		return nil, err
	}
	req := httptest.NewRequest(method, u.String(), nil).WithContext(ctx)
	req.Header = options.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if options.Credentials != nil {
		req.SetBasicAuth(options.Credentials.Username, options.Credentials.Password)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	res := w.Result()
	if badAuth {
		res.Body.Close()
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private source error"))}, nil
	}
	if missing {
		res.Header.Del("ETag")
	}
	if changed && method == "GET" && options.Headers.Get("Range") != "" && options.Headers.Get("Range") != "bytes=0-0" {
		res.Header.Set("ETag", `"changed"`)
	}
	if unknown {
		res.ContentLength = -1
		res.Header.Del("Content-Length")
	}
	if delay > 0 && method == "GET" && res.ContentLength != 1 {
		res.Body = &delayedBody{ReadCloser: res.Body, ctx: ctx, delay: delay}
	}
	return res, nil
}

type delayedBody struct {
	io.ReadCloser
	ctx   context.Context
	delay time.Duration
}

func (b *delayedBody) Read(p []byte) (int, error) {
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-timer.C:
	}
	return b.ReadCloser.Read(p[:min(len(p), 16<<10)])
}

type harness struct {
	s        *Service
	db       *store.Store
	profiles *profiles.Service
	auth     *auth.Service
	catalog  *catalog.Service
	files    *downloadfs.Store
	fetcher  *fixtureFetcher
	owner    auth.Principal
	profile  profiles.Profile
	entry    listing.Entry
	data     []byte
	limits   Limits
}

func fixture(t *testing.T, size int, limits Limits) *harness {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db.DB)
	p := profiles.New(db.DB, vault)
	owner := auth.Principal{Owner: true, CreatorID: "owner", SessionID: "owner-session", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err = db.DB.Exec(`INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at) VALUES(?,?,?,?)`, owner.SessionID, []byte("synthetic-session"), "synthetic-csrf", owner.ExpiresAt.Unix()); err != nil {
		t.Fatal(err)
	}
	profile, err := p.Create(context.Background(), profiles.Input{Name: "Synthetic", RootURL: "https://source.example/", Connections: 4}, owner)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	f := &fixtureFetcher{handler: testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{"/file.bin": {Data: data}}})}
	c := catalog.New(db.DB, p, f)
	entry := listing.Entry{ID: listing.ID(profile.ID, profile.Generation, "file.bin"), ProfileID: profile.ID, ParentID: "root", Name: "file.bin", EscapedPath: "file.bin", Kind: "file", Category: "other", Provenance: "response_headers"}
	body, _ := json.Marshal(entry)
	if _, err = db.DB.Exec(`INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?)`, profile.ID, profile.Generation, entry.ID, "root", "file.bin", string(body)); err != nil {
		t.Fatal(err)
	}
	fs, err := downloadfs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if limits.AuthInterval == 0 {
		limits.AuthInterval = 5 * time.Millisecond
	}
	if limits.CheckpointInterval == 0 {
		limits.CheckpointInterval = time.Millisecond
	}
	limits.FreeReserveBytes = -1
	h := &harness{db: db, profiles: p, auth: a, catalog: c, files: fs, fetcher: f, owner: owner, profile: profile, entry: entry, data: data, limits: limits}
	h.s = New(db.DB, p, a, c, f, events.New(a), fs, limits)
	t.Cleanup(func() { h.s.Close(); p.Close(); fs.Close(); db.Close() })
	return h
}
func (h *harness) create(t *testing.T, connections int, key string, p auth.Principal) Download {
	t.Helper()
	j, err := h.s.Create(context.Background(), p, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server", Connections: &connections}, key)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func (h *harness) start(t *testing.T) {
	t.Helper()
	if err := h.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func (h *harness) wait(t *testing.T, id string, predicate func(Download) bool) Download {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	var j Download
	for time.Now().Before(end) {
		var err error
		j, err = h.s.Get(context.Background(), h.owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(j) {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("download did not reach expected state: %+v error=%+v", j, j.Error)
	return j
}
func (h *harness) idle(t *testing.T, id string) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		h.s.op.Lock()
		active := h.s.active[id] != nil
		h.s.op.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker did not stop")
}
func (h *harness) hash(t *testing.T, id string) [32]byte {
	t.Helper()
	f, _, err := h.s.Completed(context.Background(), h.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(body)
}

func TestSequentialAndSegmentedHashes(t *testing.T) {
	for _, connections := range []int{1, 2, 4} {
		t.Run(string(rune('0'+connections)), func(t *testing.T) {
			h := fixture(t, 131079, Limits{})
			j := h.create(t, connections, "one", h.owner)
			h.start(t)
			done := h.wait(t, j.ID, func(d Download) bool {
				return d.State == "completed" || d.State == "failed" || d.State == "interrupted"
			})
			if done.State != "completed" || done.TransferredBytes != int64(len(h.data)) || done.TotalBytes == nil || *done.TotalBytes != int64(len(h.data)) || done.Connections != connections || h.hash(t, j.ID) != sha256.Sum256(h.data) {
				t.Fatalf("incorrect transfer: %+v error=%+v", done, done.Error)
			}
		})
	}
}

func TestIdempotencyAndConnectionCap(t *testing.T) {
	h := fixture(t, 100, Limits{})
	a := h.create(t, 1, "same", h.owner)
	b := h.create(t, 1, "same", h.owner)
	if a.ID != b.ID {
		t.Fatal("idempotency duplicated job")
	}
	two := 2
	_, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server", Connections: &two}, "same")
	if !errors.Is(err, ErrIdempotency) {
		t.Fatal(err)
	}
	zero := 0
	_, err = h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server", Connections: &zero}, "zero")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("explicit zero accepted", err)
	}
	_, err = h.profiles.Patch(context.Background(), h.profile.ID, map[string]json.RawMessage{"connections_per_file": []byte("1")}, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server", Connections: &two}, "cap")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("profile cap bypassed", err)
	}
}

func TestPauseRestartAndResume(t *testing.T) {
	h := fixture(t, 2<<20|17, Limits{})
	h.fetcher.delay = 2 * time.Millisecond
	j := h.create(t, 4, "resume", h.owner)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.TransferredBytes >= 32768 })
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "pause", false); err != nil {
		t.Fatal(err)
	}
	h.idle(t, j.ID)
	h.s.Close()
	h.s = New(h.db.DB, h.profiles, h.auth, h.catalog, h.fetcher, events.New(h.auth), h.files, h.limits)
	h.start(t)
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "resume", false); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool {
		return d.State == "completed" || d.State == "failed" || d.State == "interrupted"
	})
	if done.State != "completed" || h.hash(t, j.ID) != sha256.Sum256(h.data) {
		t.Fatalf("resume failed: %+v %v", done, done.Error)
	}
}

func TestMissingValidatorRequiresExplicitRestart(t *testing.T) {
	h := fixture(t, 1<<20, Limits{})
	h.fetcher.delay = 2 * time.Millisecond
	h.fetcher.withoutETag = true
	j := h.create(t, 1, "weak", h.owner)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.TransferredBytes > 0 })
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "pause", false); err != nil {
		t.Fatal(err)
	}
	h.idle(t, j.ID)
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "resume", false); !errors.Is(err, ErrRestart) {
		t.Fatalf("unsafe resume accepted: %v", err)
	}
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "resume", true); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool {
		return d.State == "completed" || d.State == "failed" || d.State == "interrupted"
	})
	if done.State != "completed" || h.hash(t, j.ID) != sha256.Sum256(h.data) {
		t.Fatalf("restart failed: %+v", done)
	}
}

func TestChangedRangeNeverPublishes(t *testing.T) {
	h := fixture(t, 131079, Limits{})
	h.fetcher.changeRanges = true
	j := h.create(t, 4, "changed", h.owner)
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "interrupted" || d.State == "failed" })
	if done.FileAvailable || done.Error == nil || done.Error.Code != "restart_required" {
		t.Fatalf("changed representation accepted: %+v", done)
	}
	if _, _, err := h.s.Completed(context.Background(), h.owner, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial published", err)
	}
}

func TestInvalidOrIgnoredRangesFallBack(t *testing.T) {
	for _, behavior := range []testfixture.Behavior{testfixture.IgnoredRange, testfixture.MalformedRange} {
		t.Run(string(behavior), func(t *testing.T) {
			h := fixture(t, 65539, Limits{})
			h.fetcher.handler = testfixture.Handler(testfixture.Config{Behavior: behavior, Files: map[string]testfixture.File{"/file.bin": {Data: h.data}}})
			j := h.create(t, 4, "fallback", h.owner)
			h.start(t)
			done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
			if done.State != "completed" || done.Connections != 1 || done.FallbackReason == nil || h.hash(t, j.ID) != sha256.Sum256(h.data) {
				t.Fatalf("fallback failed: %+v", done)
			}
		})
	}
}

func TestRevokedTokenStopsActiveJob(t *testing.T) {
	h := fixture(t, 2<<20, Limits{})
	h.fetcher.delay = 2 * time.Millisecond
	token, raw, err := h.auth.CreateToken(context.Background(), auth.TokenCreate{Name: "Synthetic", ProfileIDs: []string{h.profile.ID}, Scopes: []string{"downloads:write", "content:read"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.auth.Authenticate(context.Background(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	j := h.create(t, 1, "token", p)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.TransferredBytes > 0 })
	if err = h.auth.DeleteToken(context.Background(), token.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "cancelled" })
	if done.FileAvailable || done.Error == nil || done.Error.Code != "forbidden" {
		t.Fatalf("revoked token job: %+v", done)
	}
}

func TestQuotaReservationsAndRetainedFiles(t *testing.T) {
	h := fixture(t, 100000, Limits{StorageBudgetBytes: 150000})
	h.fetcher.delay = 10 * time.Millisecond
	a := h.create(t, 1, "a", h.owner)
	b := h.create(t, 1, "b", h.owner)
	h.start(t)
	doneA := h.wait(t, a.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	doneB := h.wait(t, b.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if doneA.State == doneB.State {
		t.Fatalf("expected one completion and one quota failure: %s %s", doneA.State, doneB.State)
	}
	failed := doneA
	if doneB.State == "failed" {
		failed = doneB
	}
	if failed.Error == nil || failed.Error.Code != "storage_full" {
		t.Fatalf("quota error: %+v", failed.Error)
	}
}

func TestExistingCompletedNameSurvives(t *testing.T) {
	h := fixture(t, 100, Limits{})
	j := h.create(t, 1, "collision", h.owner)
	dir, err := h.files.OpenJob(j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	f, err := dir.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("original"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = dir.Publish(j.Name, 8); err != nil {
		t.Fatal(err)
	}
	dir.Close()
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" })
	if done.FileAvailable {
		t.Fatal("existing output was adopted")
	}
	dir, err = h.files.OpenJob(j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	f, err = dir.OpenCompleted(j.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	if string(data) != "original" {
		t.Fatal("existing output overwritten")
	}
}

func TestUnknownSizeCap(t *testing.T) {
	h := fixture(t, 128<<10, Limits{UnknownSizeMaxBytes: 64 << 10})
	h.fetcher.unknown = true
	h.fetcher.withoutETag = true
	j := h.create(t, 1, "unknown", h.owner)
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" || d.State == "interrupted" })
	if done.State != "failed" || done.Error == nil || done.Error.Code != "storage_full" || done.FileAvailable {
		t.Fatalf("unknown size cap: %+v error=%+v", done, done.Error)
	}
}

func TestBadCredentialsWaitWithoutRetryStorm(t *testing.T) {
	h := fixture(t, 100, Limits{RetryBase: time.Millisecond})
	h.fetcher.authFailure = true
	j := h.create(t, 1, "auth", h.owner)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "waiting_for_credentials" })
	h.fetcher.mu.Lock()
	defer h.fetcher.mu.Unlock()
	if h.fetcher.calls != 1 {
		t.Fatalf("authentication retried %d requests", h.fetcher.calls)
	}
}

func TestProfileDeletionKeepsCompletedOwnerFile(t *testing.T) {
	h := fixture(t, 100, Limits{})
	j := h.create(t, 1, "keep", h.owner)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" })
	if err := h.profiles.Delete(context.Background(), h.profile.ID); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.Get(context.Background(), h.owner, j.ID)
	if err != nil || got.ProfileID != nil || !got.FileAvailable || h.hash(t, j.ID) != sha256.Sum256(h.data) {
		t.Fatalf("completed output lost: %+v %v", got, err)
	}
}

func TestPublicationIntentRecovery(t *testing.T) {
	for _, stage := range []string{"before_publish", "after_publish", "wrong_checksum"} {
		t.Run(stage, func(t *testing.T) {
			h := fixture(t, 65539, Limits{})
			j := h.create(t, 1, "recovery", h.owner)
			dir, err := h.files.OpenJob(j.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			f, err := dir.OpenPart()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write(h.data); err != nil {
				t.Fatal(err)
			}
			if err = f.Sync(); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if stage == "after_publish" {
				if err = dir.Publish(j.Name, int64(len(h.data))); err != nil {
					t.Fatal(err)
				}
			}
			hash := sha256.Sum256(h.data)
			checksum := hex.EncodeToString(hash[:])
			if stage == "wrong_checksum" {
				checksum = strings.Repeat("0", 64)
			}
			_, err = h.db.DB.Exec(`UPDATE download_jobs SET state='running',publishing=1,transferred=?,total_bytes=?,checksum=? WHERE id=?`, len(h.data), len(h.data), checksum, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			h.start(t)
			got, err := h.s.Get(context.Background(), h.owner, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "wrong_checksum" {
				if got.FileAvailable || got.State != "interrupted" {
					t.Fatalf("unverified output accepted: %+v", got)
				}
				if completed, err := dir.OpenCompleted(j.Name); err == nil {
					completed.Close()
					t.Fatal("corrupt partial was published before checksum validation")
				}
			} else if got.State != "completed" || h.hash(t, j.ID) != hash {
				t.Fatalf("publication recovery failed: %+v", got)
			}
		})
	}
}

func TestPersistedSegmentsRejectGapsOverlapAndIncorrectProgress(t *testing.T) {
	size := int64(101)
	j := job{Download: Download{Connections: 2, TotalBytes: &size, TransferredBytes: 20}}
	valid := []segment{{number: 0, start: 0, end: 50, done: 10}, {number: 1, start: 51, end: 100, done: 10}}
	if err := validateSegments(j, valid, 61); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]segment){
		func(p []segment) { p[1].start-- },
		func(p []segment) { p[1].start++ },
		func(p []segment) { p[1].end-- },
		func(p []segment) { p[1].done++ },
		func(p []segment) { p[1].number = 0 },
	} {
		parts := append([]segment(nil), valid...)
		mutate(parts)
		if !errors.Is(validateSegments(j, parts, 101), ErrRestart) {
			t.Fatalf("accepted invalid segments: %+v", parts)
		}
	}
	if !errors.Is(validateSegments(j, valid[:1], 101), ErrRestart) || !errors.Is(validateSegments(j, valid, 60), ErrRestart) {
		t.Fatal("accepted missing segment or truncated partial")
	}
}

func TestOwnerJobSurvivesLogout(t *testing.T) {
	h := fixture(t, 100000, Limits{})
	j := h.create(t, 1, "logout", h.owner)
	if _, err := h.db.DB.Exec(`DELETE FROM auth_sessions WHERE id=?`, h.owner.SessionID); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.s.load(context.Background(), j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == "completed" {
			return
		}
		if got.State == "cancelled" || got.State == "failed" {
			t.Fatalf("owner job stopped after logout: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("owner job did not complete after logout")
}

func TestRetryBackoffHonorsRetryAfterAndStops(t *testing.T) {
	for attempt := range 4 {
		delay, retry := retryDelay(&temporaryError{delay: 3 * time.Second}, time.Second, attempt)
		if !retry || delay < max(3*time.Second, time.Second*time.Duration(1<<attempt)) {
			t.Fatalf("invalid retry delay %s", delay)
		}
	}
	if _, retry := retryDelay(ErrRestart, time.Second, 0); retry {
		t.Fatal("representation change retried")
	}
	if _, retry := retryDelay(&content.Error{Status: 502, Code: "upstream_unavailable"}, time.Second, 0); !retry {
		t.Fatal("metadata transient failure was not retryable")
	}
	h := fixture(t, 100, Limits{MaxAttempts: 3, RetryBase: time.Millisecond})
	h.fetcher.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	j := h.create(t, 1, "retry", h.owner)
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" })
	h.fetcher.mu.Lock()
	defer h.fetcher.mu.Unlock()
	if h.fetcher.calls != 3 {
		t.Fatalf("bounded retry count: %d", h.fetcher.calls)
	}
}

func TestTemporaryCredentialExpiryStopsTransfer(t *testing.T) {
	h := fixture(t, 2<<20, Limits{})
	h.fetcher.delay = 5 * time.Millisecond
	profile, err := h.profiles.Patch(context.Background(), h.profile.ID, map[string]json.RawMessage{"auth_type": []byte(`"basic"`), "credentials": []byte(`{"username":"synthetic","password":"fixture","remember":false}`)}, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.profiles.Unlock(profile, profiles.LeaseID(h.owner), profiles.Credentials{Username: "synthetic", Password: "fixture"}, time.Now().Add(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	j := h.create(t, 1, "lease", h.owner)
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool {
		return d.State == "waiting_for_credentials" || d.State == "completed" || d.State == "failed"
	})
	if done.State != "waiting_for_credentials" || done.FileAvailable {
		t.Fatalf("expired credentials retained transfer authority: %+v", done)
	}
}

func TestSourceChangesStopActiveTransfer(t *testing.T) {
	for _, change := range []string{"delete", "root"} {
		t.Run(change, func(t *testing.T) {
			h := fixture(t, 2<<20, Limits{})
			h.fetcher.delay = 5 * time.Millisecond
			j := h.create(t, 1, "change", h.owner)
			h.start(t)
			h.wait(t, j.ID, func(d Download) bool { return d.TransferredBytes > 0 })
			var err error
			if change == "delete" {
				err = h.profiles.Delete(context.Background(), h.profile.ID)
			} else {
				_, err = h.profiles.Patch(context.Background(), h.profile.ID, map[string]json.RawMessage{"root_url": []byte(`"https://different.example/"`)}, h.owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := h.wait(t, j.ID, func(d Download) bool { return d.State == "cancelled" || d.State == "completed" || d.State == "failed" })
			if done.State != "cancelled" || done.FileAvailable {
				t.Fatalf("changed source continued transfer: %+v", done)
			}
		})
	}
}

func TestTokenOwnershipAndOwnerTakeover(t *testing.T) {
	h := fixture(t, 100, Limits{})
	principals := make([]auth.Principal, 2)
	for i := range principals {
		_, raw, err := h.auth.CreateToken(context.Background(), auth.TokenCreate{Name: "Synthetic", ProfileIDs: []string{h.profile.ID}, Scopes: []string{"downloads:write", "content:read"}})
		if err != nil {
			t.Fatal(err)
		}
		principals[i], err = h.auth.Authenticate(context.Background(), "", raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	j := h.create(t, 1, "own", principals[0])
	if _, err := h.s.Get(context.Background(), principals[1], j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling token read another job: %v", err)
	}
	if _, err := h.s.Action(context.Background(), principals[1], j.ID, "cancel", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling token changed another job: %v", err)
	}
	page, err := h.s.List(context.Background(), principals[1], Query{})
	if err != nil || len(page.Downloads) != 0 {
		t.Fatalf("sibling token listed another job: %+v %v", page, err)
	}
	if _, err = h.s.Action(context.Background(), principals[0], j.ID, "pause", false); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.Action(context.Background(), h.owner, j.ID, "resume", false); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.Get(context.Background(), principals[0], j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("former creator retained access after owner takeover: %v", err)
	}
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" })
	if _, _, err = h.s.Completed(context.Background(), principals[0], j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("former token retrieved owner's file", err)
	}
}

func TestUnknownAndEmptyFilesComplete(t *testing.T) {
	for _, size := range []int{0, 101} {
		h := fixture(t, size, Limits{UnknownSizeMaxBytes: 1 << 10})
		h.fetcher.unknown = true
		h.fetcher.withoutETag = true
		j := h.create(t, 1, "length", h.owner)
		h.start(t)
		done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
		if done.State != "completed" || done.TotalBytes != nil || done.TransferredBytes != int64(size) || h.hash(t, j.ID) != sha256.Sum256(h.data) {
			t.Fatalf("unknown file result: %+v", done)
		}
	}
}

func TestReplayKeepsInitialResponseAfterCompletion(t *testing.T) {
	h := fixture(t, 100, Limits{})
	original := h.create(t, 1, "original", h.owner)
	h.start(t)
	h.wait(t, original.ID, func(d Download) bool { return d.State == "completed" })
	replay := h.create(t, 1, "original", h.owner)
	a, _ := json.Marshal(original)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) || replay.State != "queued" {
		t.Fatalf("replay changed: %s -> %s", a, b)
	}
	if err := h.profiles.Delete(context.Background(), h.profile.ID); err != nil {
		t.Fatal(err)
	}
	replay = h.create(t, 1, "original", h.owner)
	b, _ = json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatal("owner replay lost after profile deletion")
	}
}

func TestUnfinishedJobLimitAllowsTerminalHistoryAndReplay(t *testing.T) {
	h := fixture(t, 0, Limits{})
	j := h.create(t, 1, "existing", h.owner)
	_, err := h.db.DB.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO download_jobs(id,profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,requested_connections,state,created_at,updated_at) SELECT printf('syntheticjob%08d',x),profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,requested_connections,state,created_at,updated_at FROM n,download_jobs WHERE id=?`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.DB.Exec(`UPDATE download_jobs SET state=CASE WHEN id<'syntheticjob00005000' THEN 'completed' WHEN id<'syntheticjob00007500' THEN 'failed' ELSE 'cancelled' END WHERE id<>?`, j.ID); err != nil {
		t.Fatal(err)
	}
	h.create(t, 1, "terminal-history", h.owner)
	if _, err = h.db.DB.Exec(`UPDATE download_jobs SET state=CASE WHEN id<'syntheticjob00000200' THEN 'queued' WHEN id<'syntheticjob00000400' THEN 'running' WHEN id<'syntheticjob00000600' THEN 'paused' WHEN id<'syntheticjob00000800' THEN 'interrupted' ELSE 'waiting_for_credentials' END WHERE id BETWEEN 'syntheticjob00000001' AND 'syntheticjob00000998'`); err != nil {
		t.Fatal(err)
	}
	if got := h.create(t, 1, "existing", h.owner); got.ID != j.ID {
		t.Fatal("limit prevented existing replay")
	}
	_, err = h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server"}, "new")
	if !errors.Is(err, ErrBusy) {
		t.Fatal("job limit was not enforced", err)
	}
	if _, err = h.s.Action(context.Background(), h.owner, "syntheticjob00009999", "retry", false); !errors.Is(err, ErrBusy) {
		t.Fatal("retry bypassed unfinished-job limit", err)
	}
}

func TestStartupReconciliationPagesChangingResults(t *testing.T) {
	h := fixture(t, 0, Limits{})
	j := h.create(t, 1, "reconcile-pages", h.owner)
	_, err := h.db.DB.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<451) INSERT INTO download_jobs(id,profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,requested_connections,state,file_available,created_at,updated_at) SELECT printf('syntheticjob%08d',x),profile_id,generation,creator_id,lease_id,owner,entry_id,escaped_path,name,destination_id,requested_connections,CASE WHEN x%2=0 THEN 'running' ELSE 'completed' END,CASE WHEN x%2=0 THEN 0 ELSE 1 END,created_at,updated_at FROM n,download_jobs WHERE id=?`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var remaining, interrupted int
	if err = h.db.DB.QueryRow(`SELECT COUNT(*) FROM download_jobs WHERE state='running' OR file_available=1`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err = h.db.DB.QueryRow(`SELECT COUNT(*) FROM download_jobs WHERE state='interrupted'`).Scan(&interrupted); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || interrupted != 225 {
		t.Fatalf("paged reconciliation skipped records: remaining=%d interrupted=%d", remaining, interrupted)
	}
}
