package updates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/upstream"
)

type doFunc func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)

func (f doFunc) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	return f(ctx, root, method, path, options)
}
func releaseResponse(tag string) *http.Response {
	return response(200, fmt.Sprintf(`{"tag_name":%q,"html_url":%q,"published_at":"2026-10-01T12:00:00Z","draft":false,"prerelease":false}`, tag, "https://github.com/themartz90/folderhug/releases/tag/"+tag))
}
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func info(version string) buildinfo.Info {
	return buildinfo.Info{Version: version, Revision: "fixture", Channel: "stable"}
}

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *testClock) add(d time.Duration) { c.mu.Lock(); c.at = c.at.Add(d); c.mu.Unlock() }
func fixtureService(t *testing.T, version string, client doFunc) (*Service, *testClock) {
	t.Helper()
	s := New(client, info(version), true)
	t.Cleanup(s.Close)
	c := &testClock{at: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	s.now = c.now
	return s, c
}

func TestDisabledAndDevelopmentDoNotRequest(t *testing.T) {
	client := doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		t.Error("unexpected network request")
		return nil, errors.New("blocked")
	})
	for _, tc := range []struct {
		version string
		enabled bool
		state   string
	}{{"v1.0.0", false, "disabled"}, {"dev", true, "development"}, {"invalid", true, "development"}} {
		s := New(client, info(tc.version), tc.enabled)
		if got := s.Status(context.Background()); got.State != tc.state || got.Latest != nil || got.CheckedAt != nil || got.Stale {
			t.Errorf("%+v", got)
		}
		s.Close()
	}
}

func TestSuccessfulCacheAndConditionalRefresh(t *testing.T) {
	var hits int
	client := doFunc(func(ctx context.Context, root *upstream.Root, method, path string, opts upstream.Options) (*http.Response, error) {
		hits++
		if root.URL().String() != "https://api.github.com/repos/themartz90/folderhug/releases/latest" || method != "GET" || path != "" || opts.Credentials != nil || opts.MaxBytes != 1<<20 || opts.Timeout != 5*time.Second {
			t.Errorf("unsafe request: %v %s %s %+v", root.URL(), method, path, opts)
		}
		if opts.Headers.Get("User-Agent") != "Folderhug" || opts.Headers.Get("X-GitHub-Api-Version") != "2026-03-10" || opts.Headers.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("headers %v", opts.Headers)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing service deadline")
		}
		if hits == 1 {
			res := releaseResponse("v0.10.0")
			res.Header.Set("ETag", `"release"`)
			return res, nil
		}
		if opts.Headers.Get("If-None-Match") != `"release"` {
			t.Error("missing validator")
		}
		return response(304, ""), nil
	})
	s, clock := fixtureService(t, "v0.9.0", client)
	first := s.Status(context.Background())
	if first.State != "available" || first.Stale || first.CheckedAt == nil {
		t.Fatal(first)
	}
	clock.add(12*time.Hour - time.Second)
	if got := s.Status(context.Background()); hits != 1 || got.State != "available" {
		t.Fatal(got, hits)
	}
	clock.add(time.Second)
	got := s.Status(context.Background())
	if hits != 2 || got.Stale || got.CheckedAt == nil || !got.CheckedAt.Equal(clock.now()) {
		t.Fatal(got, hits)
	}
}

func TestFailuresBackoffAndStaleComparison(t *testing.T) {
	for _, version := range []string{"v0.9.0", "v1.0.0"} {
		t.Run(version, func(t *testing.T) {
			hits := 0
			s, clock := fixtureService(t, version, doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
				hits++
				if hits == 1 {
					return releaseResponse("v1.0.0"), nil
				}
				return nil, errors.New("offline")
			}))
			good := s.Status(context.Background())
			clock.add(12 * time.Hour)
			stale := s.Status(context.Background())
			want := "available"
			if version == "v1.0.0" {
				want = "unknown"
			}
			if stale.State != want || !stale.Stale || stale.Latest == nil || stale.CheckedAt == nil || !stale.CheckedAt.Equal(*good.CheckedAt) || stale.RetryAt == nil {
				t.Fatal(stale)
			}
			clock.add(time.Hour - time.Second)
			s.Status(context.Background())
			if hits != 2 {
				t.Fatal(hits)
			}
			clock.add(time.Second)
			s.Status(context.Background())
			if hits != 3 {
				t.Fatal(hits)
			}
		})
	}
}

func TestRateLimitDeadline(t *testing.T) {
	for _, kind := range []string{"seconds", "date", "reset", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			hits := 0
			var clock *testClock
			s, c := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
				hits++
				res := response(429, "")
				switch kind {
				case "seconds":
					res.Header.Set("Retry-After", "7200")
				case "date":
					res.Header.Set("Retry-After", clock.now().Add(2*time.Hour).Format(http.TimeFormat))
				case "reset":
					res.Header.Set("X-RateLimit-Reset", fmt.Sprint(clock.now().Add(2*time.Hour).Unix()))
				case "invalid":
					res.Header.Set("Retry-After", "bogus")
					res.Header.Set("X-RateLimit-Reset", "-1")
				}
				return res, nil
			}))
			clock = c
			got := s.Status(context.Background())
			want := clock.now().Add(2 * time.Hour)
			if kind == "invalid" {
				want = clock.now().Add(time.Hour)
			}
			if got.State != "unknown" || got.RetryAt == nil || !got.RetryAt.Equal(want) {
				t.Fatal(got, want)
			}
			clock.add(59 * time.Minute)
			s.Status(context.Background())
			if hits != 1 {
				t.Fatal(hits)
			}
		})
	}
}

func TestConcurrentRefreshSurvivesFirstRequestCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	s, _ := fixtureService(t, "v1.0.0", doFunc(func(ctx context.Context, _ *upstream.Root, _, _ string, _ upstream.Options) (*http.Response, error) {
		hits.Add(1)
		close(started)
		select {
		case <-release:
			return releaseResponse("v1.1.0"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan Status, 1)
	go func() { first <- s.Status(ctx) }()
	<-started
	results := make(chan Status, 12)
	for range 12 {
		go func() { results <- s.Status(context.Background()) }()
	}
	cancel()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter stuck")
	}
	close(release)
	for range 12 {
		select {
		case got := <-results:
			if got.State != "available" || got.Stale {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("refresh stuck")
		}
	}
	if hits.Load() != 1 {
		t.Fatal(hits.Load())
	}
}

func TestPayloadValidationAndMissingRelease(t *testing.T) {
	valid := func() string { res := releaseResponse("v1.1.0"); b, _ := io.ReadAll(res.Body); return string(b) }()
	for _, body := range []string{
		strings.Replace(valid, `"draft":false`, `"draft":true`, 1), strings.Replace(valid, `"prerelease":false`, `"prerelease":true`, 1),
		strings.ReplaceAll(valid, "v1.1.0", "v1.1.0-rc.1"), strings.Replace(valid, "https://github.com/", "http://github.com/", 1),
		strings.Replace(valid, "github.com/", "github.com:443/", 1), strings.Replace(valid, "github.com/", "synthetic-owner@github.com/", 1),
		strings.Replace(valid, "releases/tag/v1.1.0", "releases/tag/v1.0.0", 1), strings.Replace(valid, "releases/tag/v1.1.0", "releases/tag/v1.1.0?x=1", 1),
		strings.Replace(valid, "releases/tag/v1.1.0", "releases/tag/v1.1.0#x", 1), strings.Replace(valid, "themartz90/folderhug", "unrelated-owner/folderhug", 1),
		strings.Replace(valid, `"draft":false,`, "", 1), strings.Replace(valid, "2026-10-01T12:00:00Z", "yesterday", 1), valid + ` {}`, `{}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, _ := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
				return response(200, body), nil
			}))
			if got := s.Status(context.Background()); got.State != "unknown" || got.Latest != nil || got.CheckedAt != nil {
				t.Fatal(got)
			}
		})
	}
	for _, code := range []int{404, 304, 500} {
		s, _ := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
			return response(code, ""), nil
		}))
		if got := s.Status(context.Background()); got.State != "unknown" || got.Latest != nil {
			t.Fatal(code, got)
		}
	}
}

func TestServiceBoundsMockResponseAndCloses(t *testing.T) {
	body := &countedBody{Reader: strings.NewReader(strings.Repeat(" ", 2<<20))}
	s, _ := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		res := response(200, "")
		res.Body = body
		return res, nil
	}))
	if got := s.Status(context.Background()); got.State != "unknown" || body.read > 1<<20+1 || !body.closed.Load() {
		t.Fatal(got, body.read, body.closed.Load())
	}
}

type countedBody struct {
	io.Reader
	read   int
	closed atomic.Bool
}

func (b *countedBody) Read(p []byte) (int, error) { n, e := b.Reader.Read(p); b.read += n; return n, e }
func (b *countedBody) Close() error               { b.closed.Store(true); return nil }

func TestShutdownCancelsRequestAndNoNewRequests(t *testing.T) {
	started := make(chan struct{})
	s, _ := fixtureService(t, "v1.0.0", doFunc(func(ctx context.Context, _ *upstream.Root, _, _ string, _ upstream.Options) (*http.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	done := make(chan Status)
	go func() { done <- s.Status(context.Background()) }()
	<-started
	s.Close()
	select {
	case got := <-done:
		if got.State != "unknown" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel")
	}
	s.Status(context.Background())
}

type blockedBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *blockedBody) Read([]byte) (int, error) { <-b.closed; return 0, errors.New("closed") }
func (b *blockedBody) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }

func TestDeadlineIncludesInjectedResponseBody(t *testing.T) {
	body := &blockedBody{closed: make(chan struct{})}
	s, _ := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		res := response(200, "")
		res.Body = body
		return res, nil
	}))
	start := time.Now()
	got := s.Status(context.Background())
	if got.State != "unknown" || time.Since(start) > 7*time.Second {
		t.Fatal(got, time.Since(start))
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("timed-out body was not closed")
	}
}

func TestInvalidRefreshPreservesGoodDataAndValidator(t *testing.T) {
	hits := 0
	s, clock := fixtureService(t, "v1.0.0", doFunc(func(_ context.Context, _ *upstream.Root, _, _ string, opts upstream.Options) (*http.Response, error) {
		hits++
		switch hits {
		case 1:
			res := releaseResponse("v1.1.0")
			res.Header.Set("ETag", `"good"`)
			return res, nil
		case 2:
			res := response(200, `{"tag_name":"garbage"}`)
			res.Header.Set("ETag", `"bad"`)
			return res, nil
		default:
			if opts.Headers.Get("If-None-Match") != `"good"` {
				t.Error("invalid response replaced validator")
			}
			return response(304, ""), nil
		}
	}))
	first := s.Status(context.Background())
	clock.add(12 * time.Hour)
	got := s.Status(context.Background())
	if !got.Stale || got.State != "available" || got.Latest.Version != first.Latest.Version || !got.CheckedAt.Equal(*first.CheckedAt) {
		t.Fatal(got)
	}
	// A returned value is not a mutable cache handle.
	got.Latest.Version = "corrupted"
	*got.CheckedAt = time.Time{}
	clock.add(time.Hour)
	got = s.Status(context.Background())
	if got.Stale || got.Latest.Version != "v1.1.0" || !got.CheckedAt.Equal(clock.now()) {
		t.Fatal(got)
	}
}

func TestCancelledStatusDoesNotStartRequest(t *testing.T) {
	s, _ := fixtureService(t, "v1.0.0", doFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		t.Error("request after cancellation")
		return nil, errors.New("blocked")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Status(ctx)
}
