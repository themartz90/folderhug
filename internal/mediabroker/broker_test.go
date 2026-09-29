package mediabroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/mediaworker"
	"github.com/themartz90/folderhug/internal/upstream"
)

type fetchFunc func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)

func (f fetchFunc) Do(ctx context.Context, r *upstream.Root, m, p string, o upstream.Options) (*http.Response, error) {
	return f(ctx, r, m, p, o)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func testBroker(t *testing.T, size int64) (*Broker, *lease) {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 32)
	b, err := New(nil, nil, nil, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := upstream.NewRoot("https://example.invalid/library/", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	t.Cleanup(cancel)
	deadline, _ := ctx.Deadline()
	j := &lease{id: "job_one", token: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), ctx: ctx, cancel: cancel, root: root, path: "video%20one.mp4", credentials: &upstream.Credentials{Username: "source-user", Password: "source-password"}, size: size, etag: `"source-version"`, expires: deadline, ready: true, transfer: make(chan struct{}, 1), result: make(chan []byte, 1)}
	b.active = j
	b.fetcher = fetchFunc(func(ctx context.Context, root *upstream.Root, method, path string, opts upstream.Options) (*http.Response, error) {
		if root != j.root || path != j.path || method != "GET" || opts.Credentials != j.credentials {
			t.Error("source binding changed")
		}
		if opts.Headers.Get("Authorization") != "" || opts.Headers.Get("Cookie") != "" {
			t.Error("worker headers reached source")
		}
		a, z, ok := boundedRange(opts.Headers.Get("Range"), j.size, byteBudget)
		if !ok {
			return nil, ErrUnavailable
		}
		return sourceResponse(j, a, z, io.LimitReader(zeroReader{}, z-a+1)), nil
	})
	return b, j
}
func sourceResponse(j *lease, a, z int64, body io.Reader) *http.Response {
	r := &http.Response{StatusCode: 206, ContentLength: z - a + 1, Header: make(http.Header), Body: io.NopCloser(body)}
	r.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, z, j.size))
	r.Header.Set("Content-Length", fmt.Sprint(z-a+1))
	r.Header.Set("ETag", j.etag)
	r.Header.Set("Set-Cookie", "private=value")
	r.Header.Set("Authorization", "Basic source-secret")
	return r
}
func brokerRequest(t *testing.T, b *Broker, method, path, token, rangeHeader, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if rangeHeader != "" {
		r.Header.Set("Range", rangeHeader)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}
func claimJob(t *testing.T, b *Broker, j *lease) {
	t.Helper()
	w := brokerRequest(t, b, "POST", "/claim", b.key, "", "", nil)
	if w.Code != 200 {
		t.Fatalf("claim %d", w.Code)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 5 || result["id"] != j.id || result["token"] != j.token || strings.Contains(w.Body.String(), j.credentials.Username) || strings.Contains(w.Body.String(), j.credentials.Password) || strings.Contains(w.Body.String(), j.path) {
		t.Fatal("claim exposed source configuration or wrong capability")
	}
}

func TestClaimAndCapabilities(t *testing.T) {
	b, j := testBroker(t, 100)
	if w := brokerRequest(t, b, "POST", "/claim", "wrong", "", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil); w.Code != 401 {
		t.Fatal("unclaimed content allowed")
	}
	claimJob(t, b, j)
	if w := brokerRequest(t, b, "POST", "/claim", b.key, "", "", nil); w.Code != 204 {
		t.Fatal("claimed twice")
	}
	for _, tc := range []struct{ path, token string }{{"/jobs/other/content", j.token}, {"/jobs/" + j.id + "/content", b.key}, {"/jobs/" + j.id + "/content", "other-job-token"}, {"/jobs/http:%2F%2Fexample.invalid/content", j.token}} {
		w := brokerRequest(t, b, "GET", tc.path, tc.token, "bytes=0-0", "", nil)
		if w.Code == 206 {
			t.Fatal("cross-job or malformed path allowed")
		}
	}
	w := brokerRequest(t, b, "POST", "/jobs/"+j.id+"/result", j.token, "", "application/json", strings.NewReader(`{"state":"unavailable"}`))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil); w.Code != 401 {
		t.Fatal("result token replay allowed")
	}
	if w := brokerRequest(t, b, "POST", "/jobs/"+j.id+"/result", j.token, "", "application/json", strings.NewReader(`{"state":"unavailable"}`)); w.Code != 401 {
		t.Fatal("result replay allowed")
	}
}

func TestProbeAndTotalInputBudget(t *testing.T) {
	b, j := testBroker(t, byteBudget+1024)
	if err := b.probe(j); err != nil {
		t.Fatal(err)
	}
	if j.consumed != 1 {
		t.Fatal("probe excluded from budget")
	}
	claimJob(t, b, j)
	head := brokerRequest(t, b, "HEAD", "/jobs/"+j.id+"/content", j.token, "bytes=0-", "", nil)
	if head.Code != 206 || head.Header().Get("Content-Length") != fmt.Sprint(byteBudget-1) || head.Body.Len() != 0 || j.consumed != 1 {
		t.Fatal("HEAD consumed bytes or failed to clamp")
	}
	response := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content?url=https://attacker.invalid/&path=other", j.token, "bytes=0-", "", nil)
	if response.Code != 206 || int64(response.Body.Len()) != byteBudget-1 || j.consumed != byteBudget {
		t.Fatalf("budget %d, response %d %d", j.consumed, response.Code, response.Body.Len())
	}
	for _, name := range []string{"Authorization", "Set-Cookie"} {
		if response.Header().Get(name) != "" {
			t.Fatal("source header leaked")
		}
	}
	if w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil); w.Code != 413 || j.ctx.Err() == nil {
		t.Fatal("exhausted budget allowed")
	}
}

func TestWrongRangesAndRepresentations(t *testing.T) {
	for _, raw := range []string{"bytes=0-1,2-3", "bytes=+1-2", "bytes=100-", "bytes=2-1", "items=0-1"} {
		b, j := testBroker(t, 100)
		claimJob(t, b, j)
		w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, raw, "", nil)
		if w.Code != 416 {
			t.Fatalf("%s: %d", raw, w.Code)
		}
	}
	for _, mutation := range []func(*http.Response){func(r *http.Response) { r.StatusCode = 200 }, func(r *http.Response) { r.Header.Set("ETag", `"changed"`) }, func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") }, func(r *http.Response) { r.Header.Set("Content-Range", "bytes 1-1/100") }, func(r *http.Response) { r.ContentLength = 50 }} {
		b, j := testBroker(t, 100)
		claimJob(t, b, j)
		b.fetcher = fetchFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
			r := sourceResponse(j, 0, 0, strings.NewReader("x"))
			mutation(r)
			return r, nil
		})
		w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil)
		if w.Code != 502 || j.ctx.Err() == nil {
			t.Fatal("invalid source representation allowed")
		}
	}
}

func TestExpiryCancellationAndPendingRevocation(t *testing.T) {
	b, j := testBroker(t, 100)
	claimJob(t, b, j)
	j.cancel()
	if w := brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil); w.Code != 401 {
		t.Fatal("expired capability allowed")
	}
	b, j = testBroker(t, 100)
	claimJob(t, b, j)
	var calls atomic.Int64
	b.fetcher = fetchFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		calls.Add(1)
		return sourceResponse(j, 0, 0, strings.NewReader("x")), nil
	})
	j.transfer <- struct{}{}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- brokerRequest(t, b, "GET", "/jobs/"+j.id+"/content", j.token, "bytes=0-0", "", nil) }()
	time.Sleep(20 * time.Millisecond)
	w := brokerRequest(t, b, "POST", "/jobs/"+j.id+"/result", j.token, "", "application/json", strings.NewReader(`{"state":"unavailable"}`))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	<-j.transfer
	select {
	case response := <-done:
		if response.Code == 206 || calls.Load() != 0 {
			t.Fatal("waiting request accessed source after result revoked lease")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting request survived revocation")
	}
}

func TestBrokerWorkerIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket deployment test")
	}
	if os.Getenv("MEDIAWORKER_FFMPEG_INTEGRATION") != "1" {
		t.Skip("opt-in installed FFmpeg check")
	}
	executable := os.Getenv("FFMPEG_PATH")
	if executable == "" {
		executable = "ffmpeg"
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "video.mp4")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=2", "-t", "1", "-an", "-c:v", "mpeg4", "-threads", "1", video}
	if output, err := exec.Command(executable, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	data, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	b, j := testBroker(t, int64(len(data)))
	b.fetcher = fetchFunc(func(_ context.Context, root *upstream.Root, method, path string, opts upstream.Options) (*http.Response, error) {
		if root != j.root || path != j.path || opts.Credentials != j.credentials || method != "GET" {
			t.Error("worker changed source identity")
		}
		a, z, ok := boundedRange(opts.Headers.Get("Range"), j.size, byteBudget)
		if !ok {
			return nil, ErrUnavailable
		}
		return sourceResponse(j, a, z, bytes.NewReader(data[a:z+1])), nil
	})
	if err := b.probe(j); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "broker.sock")
	if err := b.Start(socket); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	keyPath := filepath.Join(dir, "worker.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- mediaworker.Run(ctx, mediaworker.Config{SocketPath: socket, KeyPath: keyPath, FFmpegPath: executable})
	}()
	select {
	case poster := <-j.result:
		if len(poster) < 3 || poster[0] != 0xff || poster[1] != 0xd8 {
			t.Fatal("missing JPEG")
		}
	case <-j.ctx.Done():
		t.Fatal("worker did not return poster")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestResultValidationAndHeaderAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		contentType, body string
		status            int
	}{
		{"application/json", `{"state":"unavailable","detail":"private"}`, 400},
		{"application/json", `{"state":"unavailable"}{}`, 400},
		{"image/jpeg", "invalid", 400},
		{"text/plain", "unavailable", 415},
		{"image/jpeg", strings.Repeat("x", (1<<20)+1), 413},
	} {
		b, j := testBroker(t, 100)
		claimJob(t, b, j)
		w := brokerRequest(t, b, "POST", "/jobs/"+j.id+"/result", j.token, "", tc.contentType, strings.NewReader(tc.body))
		if w.Code != tc.status || j.used {
			t.Fatalf("invalid result: %d, used=%v", w.Code, j.used)
		}
	}
	b, j := testBroker(t, 100)
	claimJob(t, b, j)
	for _, name := range []string{"Authorization", "Range", "If-Range"} {
		r := httptest.NewRequest("GET", "/jobs/"+j.id+"/content", nil)
		r.Header.Set("Authorization", "Bearer "+j.token)
		r.Header.Set("Range", "bytes=0-0")
		r.Header.Set("If-Range", j.etag)
		r.Header.Add(name, r.Header.Get(name))
		w := httptest.NewRecorder()
		b.Handler().ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 401 {
			t.Fatalf("duplicate %s allowed: %d", name, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/jobs/"+j.id+"/content", nil)
	r.Header.Set("Authorization", "Bearer "+j.token)
	r.Header.Set("If-Range", `"wrong"`)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != 412 {
		t.Fatal("mismatched If-Range allowed")
	}
}

func TestRequestCancellationStopsSource(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		b, j := testBroker(t, 100)
		claimJob(t, b, j)
		if expiry {
			j.cancel()
			j.ctx, j.cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			t.Cleanup(j.cancel)
		}
		started := make(chan struct{})
		stopped := make(chan struct{})
		b.fetcher = fetchFunc(func(ctx context.Context, _ *upstream.Root, _, _ string, _ upstream.Options) (*http.Response, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := httptest.NewRequest("GET", "/jobs/"+j.id+"/content", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+j.token)
		done := make(chan struct{})
		go func() { b.Handler().ServeHTTP(httptest.NewRecorder(), r); close(done) }()
		<-started
		if !expiry {
			cancel()
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("source survived cancellation")
		}
		<-done
	}
}

func TestSocketDoesNotReplaceExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.sock")
	if err := os.WriteFile(path, []byte("existing data"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := testBroker(t, 100)
	if err := b.Start(path); err == nil {
		b.Close()
		t.Fatal("existing file replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing data" {
		t.Fatal("existing data changed")
	}
}
