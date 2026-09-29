package mediabroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/upstream"
)

type generationFixture struct {
	b       *Broker
	a       *auth.Service
	p       *profiles.Service
	owner   auth.Principal
	profile profiles.Profile
	request thumbnails.VideoRequest
	calls   atomic.Int64
}

func newGenerationFixture(t *testing.T, etag *string) *generationFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := auth.New(db.DB)
	if err = a.Setup(ctx, "synthetic broker owner password"); err != nil {
		t.Fatal(err)
	}
	cookie, _, err := a.Login(ctx, "synthetic broker owner password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Authenticate(ctx, cookie, "")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	p := profiles.New(db.DB, vault)
	t.Cleanup(p.Close)
	profile, err := p.Create(ctx, profiles.Input{Name: "Synthetic video", RootURL: "https://video.example.test/root/", AuthType: "basic", Credentials: &profiles.Credentials{Username: "fixture-user", Password: "fixture-password"}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	f := &generationFixture{a: a, p: p, owner: owner, profile: profile}
	handler := testfixture.Handler(testfixture.Config{Username: "fixture-user", Password: "fixture-password", Files: map[string]testfixture.File{"/root/video.mp4": {Data: []byte("synthetic-video-content"), ContentType: "video/mp4"}}})
	fetcher := fetchFunc(func(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
		f.calls.Add(1)
		target, err := root.Resolve(path)
		if err != nil {
			return nil, err
		}
		r, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
		if err != nil {
			return nil, err
		}
		if options.Headers != nil {
			r.Header = options.Headers.Clone()
		}
		if options.Credentials != nil {
			r.SetBasicAuth(options.Credentials.Username, options.Credentials.Password)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		res := w.Result()
		res.Request = r
		if etag != nil {
			res.Header.Set("ETag", *etag)
		}
		return res, nil
	})
	c := catalog.New(db.DB, p, fetcher)
	page, err := c.Browse(ctx, profile.ID, profiles.LeaseID(owner), "root", catalog.Query{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("browse: entries=%d err=%v", len(page.Entries), err)
	}
	f.request = thumbnails.VideoRequest{ProfileID: profile.ID, EntryID: page.Entries[0].ID, Generation: profile.Generation, Principal: owner}
	f.b, err = New(p, a, c, fetcher, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.b.Close() })
	return f
}

type generationResult struct {
	data []byte
	err  error
}
type generationClaim struct {
	ID, Token, ETag string
	Size            int64
}

func (f *generationFixture) start(t *testing.T, request thumbnails.VideoRequest) <-chan generationResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan generationResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		data, err := f.b.Generate(ctx, request)
		done <- generationResult{data, err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("Generate did not stop after cleanup cancellation")
		}
	})
	return done
}

func awaitGeneration(t *testing.T, done <-chan generationResult) generationResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not finish")
		return generationResult{}
	}
}

func (f *generationFixture) claim(t *testing.T, done <-chan generationResult) generationClaim {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		w := brokerRequest(t, f.b, "POST", "/claim", f.b.key, "", "", nil)
		if w.Code == 200 {
			var claim generationClaim
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &claim); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 5 || claim.ID == "" || claim.Token == "" || claim.ETag == "" || claim.Size != int64(len("synthetic-video-content")) {
				t.Fatal("invalid or overexposed worker claim")
			}
			return claim
		}
		if w.Code != 204 {
			t.Fatalf("claim status %d", w.Code)
		}
		select {
		case result := <-done:
			t.Fatalf("Generate ended before claim: %v", result.err)
		case <-deadline.C:
			t.Fatal("worker claim did not become available")
		case <-tick.C:
		}
	}
}

func TestGenerateWorkerRoundTrip(t *testing.T) {
	f := newGenerationFixture(t, nil)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	// Result delivery also cancels the lease. Every accepted result must win
	// over that cancellation, including when both are ready in Generate.
	for i := 0; i < 24; i++ {
		done := f.start(t, f.request)
		claim := f.claim(t, done)
		content := brokerRequest(t, f.b, "GET", "/jobs/"+claim.ID+"/content", claim.Token, "bytes=0-8", "", nil)
		if content.Code != 206 || content.Body.String() != "synthetic" || content.Header().Get("Content-Range") != "bytes 0-8/23" {
			t.Fatalf("content status=%d range=%q body=%q", content.Code, content.Header().Get("Content-Range"), content.Body.String())
		}
		posted := brokerRequest(t, f.b, "POST", "/jobs/"+claim.ID+"/result", claim.Token, "", "image/jpeg", bytes.NewReader(encoded.Bytes()))
		if posted.Code != 204 {
			t.Fatalf("result status %d", posted.Code)
		}
		result := awaitGeneration(t, done)
		if result.err != nil || !bytes.Equal(result.data, encoded.Bytes()) {
			t.Fatalf("accepted JPEG lost: %v", result.err)
		}
		if replay := brokerRequest(t, f.b, "GET", "/jobs/"+claim.ID+"/content", claim.Token, "bytes=0-0", "", nil); replay.Code != 401 {
			t.Fatal("completed capability remained valid")
		}
	}
}

func (f *generationFixture) token(t *testing.T, profileID string, scopes ...string) auth.Principal {
	t.Helper()
	_, raw, err := f.a.CreateToken(context.Background(), auth.TokenCreate{Name: "broker test", ProfileIDs: []string{profileID}, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.a.Authenticate(context.Background(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGenerateRejectsUnauthorizedAndDisabled(t *testing.T) {
	f := newGenerationFixture(t, nil)
	other, err := f.p.Create(context.Background(), profiles.Input{Name: "Other", RootURL: "https://other.example.test/root/"}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		principal auth.Principal
	}{
		{"foreign profile", f.token(t, other.ID, "content:read")},
		{"browse only", f.token(t, f.profile.ID, "browse:read")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := f.request
			request.Principal = tc.principal
			before := f.calls.Load()
			result := awaitGeneration(t, f.start(t, request))
			if !errors.Is(result.err, ErrUnavailable) || len(result.data) != 0 || f.calls.Load() != before {
				t.Fatal("unauthorized generation fetched content or succeeded")
			}
		})
	}
	if _, err = f.p.Patch(context.Background(), f.profile.ID, map[string]json.RawMessage{"previews_enabled": json.RawMessage(`false`)}, f.owner); err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	result := awaitGeneration(t, f.start(t, f.request))
	if !errors.Is(result.err, ErrUnavailable) || f.calls.Load() != before {
		t.Fatal("disabled generation fetched source or succeeded")
	}
}

func TestGenerateCancelsWithAuthorityChanges(t *testing.T) {
	for _, change := range []string{"lock", "root change", "revoke token"} {
		t.Run(change, func(t *testing.T) {
			f := newGenerationFixture(t, nil)
			request := f.request
			if change == "revoke token" {
				request.Principal = f.token(t, f.profile.ID, "content:read")
				if err := f.p.Unlock(f.profile, profiles.LeaseID(request.Principal), profiles.Credentials{Username: "fixture-user", Password: "fixture-password"}, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			done := f.start(t, request)
			claim := f.claim(t, done)
			switch change {
			case "lock":
				f.p.Lock(f.profile.ID, profiles.LeaseID(request.Principal))
			case "root change":
				if _, err := f.p.Patch(context.Background(), f.profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://video.example.test/changed/"`)}, f.owner); err != nil {
					t.Fatal(err)
				}
			case "revoke token":
				if err := f.a.DeleteToken(context.Background(), request.Principal.CreatorID); err != nil {
					t.Fatal(err)
				}
			}
			result := awaitGeneration(t, done)
			if !errors.Is(result.err, ErrUnavailable) || len(result.data) != 0 {
				t.Fatalf("generation survived %s: %v", change, result.err)
			}
			if w := brokerRequest(t, f.b, "GET", "/jobs/"+claim.ID+"/content", claim.Token, "bytes=0-0", "", nil); w.Code != 401 {
				t.Fatal("canceled content capability remained valid")
			}
			if w := brokerRequest(t, f.b, "POST", "/jobs/"+claim.ID+"/result", claim.Token, "", "image/jpeg", bytes.NewReader([]byte{0xff, 0xd8, 0xff})); w.Code != 401 {
				t.Fatal("canceled result capability remained valid")
			}
		})
	}
}

func TestGenerateRequiresStrongValidator(t *testing.T) {
	for _, etag := range []string{"", `W/"weak-version"`} {
		t.Run(etag, func(t *testing.T) {
			f := newGenerationFixture(t, &etag)
			result := awaitGeneration(t, f.start(t, f.request))
			if !errors.Is(result.err, ErrUnavailable) || len(result.data) != 0 {
				t.Fatal("generation accepted missing or weak validator")
			}
			if w := brokerRequest(t, f.b, "POST", "/claim", f.b.key, "", "", nil); w.Code != 204 {
				t.Fatal("invalid source was exposed to worker")
			}
		})
	}
}
