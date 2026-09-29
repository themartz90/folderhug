package content

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/upstream"
)

type fetchFunc func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)

func (f fetchFunc) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	return f(ctx, root, method, path, options)
}

func response(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, ContentLength: int64(len(body)), Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func source(t *testing.T) Source {
	t.Helper()
	root, err := upstream.NewRoot("https://source.example/files/", false)
	if err != nil {
		t.Fatal(err)
	}
	return Source{Root: root, Path: "name.bin", Filename: "name.bin", Credentials: &upstream.Credentials{Username: "synthetic", Password: "fixture"}}
}

func serviceResponse(res *http.Response) *Service {
	return New(fetchFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		return res, nil
	}), Limits{})
}

func errorCode(t *testing.T, err error, code string, status int) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Status != status {
		t.Fatalf("wanted %s/%d, got %v", code, status, err)
	}
}

func TestAttachmentHeadersAndFilename(t *testing.T) {
	res := response(200, "<svg onload='alert(1)'/>", http.Header{
		"Content-Type": {"image/svg+xml"}, "Set-Cookie": {"secret=value"}, "Www-Authenticate": {"Basic private"}, "Location": {"https://private.example/"}, "Content-Disposition": {"inline; filename=hostile.svg"}, "Etag": {`"v1"`}, "Last-Modified": {"Fri, 02 Jan 2026 12:30:00 GMT"},
	})
	src := source(t)
	src.Filename = "../unsafe\\café\r\nname.svg"
	w := httptest.NewRecorder()
	err := serviceResponse(res).Serve(w, httptest.NewRequest("GET", "/content", nil), src, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Body.String() != "<svg onload='alert(1)'/>" {
		t.Fatal("attachment changed")
	}
	for _, name := range []string{"Set-Cookie", "WWW-Authenticate", "Location"} {
		if w.Header().Get(name) != "" {
			t.Errorf("source header forwarded: %s", name)
		}
	}
	disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != "caféname.svg" {
		t.Fatalf("unsafe disposition %q", w.Header().Get("Content-Disposition"))
	}
	if w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("active attachment response lacks safeguards")
	}
}

func TestPreviewSniffsBody(t *testing.T) {
	for _, tc := range []struct {
		name, mime, body string
		allowed          bool
	}{
		{"jpeg", "image/jpeg", string([]byte{0xff, 0xd8, 0xff, 0xdb, 1, 2, 3}), true},
		{"reported-svg", "image/svg+xml", "<svg/>", false},
		{"html-spoof", "image/png", "<!DOCTYPE html><html><script>alert(1)</script></html>", false},
		{"svg-spoof", "image/jpeg", "<svg xmlns='http://www.w3.org/2000/svg'/>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			err := serviceResponse(response(200, tc.body, http.Header{"Content-Type": {tc.mime}})).Serve(w, httptest.NewRequest("GET", "/", nil), source(t), true)
			if tc.allowed {
				if err != nil || w.Body.String() != tc.body || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") {
					t.Fatalf("safe preview: %v", err)
				}
			} else {
				errorCode(t, err, "unsupported_media", 415)
				if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" {
					t.Fatal("unsafe response committed")
				}
			}
		})
	}
}

type eofClosesBody struct {
	data []byte
	done bool
}

func (b *eofClosesBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, context.Canceled
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	if len(b.data) == 0 {
		b.done = true
		return n, io.EOF
	}
	return n, nil
}
func (b *eofClosesBody) Close() error { return nil }

func TestPreviewFinishesAfterSniffEOF(t *testing.T) {
	for _, length := range []int64{7, -1} {
		jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 1, 2, 3}
		res := response(200, string(jpeg), http.Header{"Content-Type": {"image/jpeg"}})
		res.Body = &eofClosesBody{data: jpeg}
		res.ContentLength = length
		w := httptest.NewRecorder()
		if err := serviceResponse(res).Serve(w, httptest.NewRequest("GET", "/", nil), source(t), true); err != nil || w.Body.String() != string(jpeg) {
			t.Fatalf("complete sniffed preview: %v", err)
		}
	}
}

func TestRanges(t *testing.T) {
	for _, tc := range []struct {
		name, requested, actual string
		length                  int64
		body                    string
		ok                      bool
	}{
		{"closed", "bytes=2-4", "bytes 2-4/8", 3, "234", true},
		{"open", "bytes=5-", "bytes 5-7/8", 3, "567", true},
		{"suffix", "bytes=-3", "bytes 5-7/8", 3, "567", true},
		{"clamped", "bytes=5-99", "bytes 5-7/8", 3, "567", true},
		{"wrong-offset", "bytes=2-4", "bytes 3-5/8", 3, "345", false},
		{"wrong-total", "bytes=2-4", "bytes 2-4/4", 3, "234", false},
		{"unknown-total", "bytes=2-4", "bytes 2-4/*", 3, "234", false},
		{"wrong-length", "bytes=2-4", "bytes 2-4/8", 2, "23", false},
		{"unrequested", "", "bytes 2-4/8", 3, "234", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := response(206, tc.body, http.Header{"Content-Range": {tc.actual}})
			res.ContentLength = tc.length
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Range", tc.requested)
			w := httptest.NewRecorder()
			err := serviceResponse(res).Serve(w, r, source(t), false)
			if tc.ok {
				if err != nil || w.Code != 206 || w.Body.String() != tc.body || w.Header().Get("Content-Range") != tc.actual {
					t.Fatalf("range: %v", err)
				}
			} else {
				errorCode(t, err, "upstream_unavailable", 502)
				if w.Body.Len() != 0 {
					t.Fatal("invalid range committed")
				}
			}
		})
	}
}

func TestIgnoredAndInvalidRanges(t *testing.T) {
	for _, value := range []string{"bytes=0-1,4-5", "bytes=-0", "bytes=3-2", "bytes=+1-3", "items=0-1", "bytes=9223372036854775808-"} {
		s := New(fetchFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
			t.Fatal("invalid range reached source")
			return nil, nil
		}), Limits{})
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", value)
		err := s.Serve(httptest.NewRecorder(), r, source(t), false)
		errorCode(t, err, "invalid_request", 400)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=1-2")
	w := httptest.NewRecorder()
	err := serviceResponse(response(200, "full-body", nil)).Serve(w, r, source(t), false)
	if err != nil || w.Code != 200 || w.Body.String() != "full-body" || w.Header().Get("Content-Range") != "" {
		t.Fatalf("ignored range was changed: %v", err)
	}
}

func TestChangedCompressedAndConditionalResponses(t *testing.T) {
	for _, tc := range []struct {
		name           string
		headers        http.Header
		expected, code string
		status         int
	}{
		{"changed", http.Header{"Etag": {`"new"`}}, `"old"`, "representation_changed", 412},
		{"compressed", http.Header{"Content-Encoding": {"gzip"}}, "", "upstream_unavailable", 502},
		{"wrong-header-length", http.Header{"Content-Length": {"99"}}, "", "upstream_unavailable", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := source(t)
			src.ExpectedETag = tc.expected
			w := httptest.NewRecorder()
			err := serviceResponse(response(200, "body", tc.headers)).Serve(w, httptest.NewRequest("GET", "/", nil), src, false)
			errorCode(t, err, tc.code, tc.status)
			if w.Body.Len() != 0 {
				t.Fatal("invalid representation committed")
			}
		})
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", `"same"`)
	r.Header.Set("Authorization", "Bearer client-secret")
	s := New(fetchFunc(func(_ context.Context, _ *upstream.Root, _, _ string, opts upstream.Options) (*http.Response, error) {
		if opts.Headers.Get("If-None-Match") != `"same"` || opts.Headers.Get("Authorization") != "" || opts.Credentials.Password != "fixture" {
			t.Fatal("wrong credential or conditional forwarding")
		}
		return response(304, "", http.Header{"Etag": {`"same"`}, "Set-Cookie": {"private"}}), nil
	}), Limits{})
	w := httptest.NewRecorder()
	if err := s.Serve(w, r, source(t), false); err != nil || w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("304: %v", err)
	}
}

func TestRangeUnsatisfied(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=20-")
	w := httptest.NewRecorder()
	err := serviceResponse(response(416, "private error", http.Header{"Content-Range": {"bytes */10"}})).Serve(w, r, source(t), false)
	errorCode(t, err, "range_not_satisfiable", 416)
	if err.(*Error).Headers.Get("Content-Range") != "bytes */10" || w.Body.Len() != 0 {
		t.Fatal("source error exposed")
	}
}

func TestMetadataHEADAndFallback(t *testing.T) {
	for _, status := range []int{200, 206} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls int
			s := New(fetchFunc(func(_ context.Context, _ *upstream.Root, method, _ string, opts upstream.Options) (*http.Response, error) {
				calls++
				if calls == 1 {
					if method != "HEAD" {
						t.Fatal("metadata did not start with HEAD")
					}
					return response(405, "", nil), nil
				}
				if method != "GET" || opts.Headers.Get("Range") != "bytes=0-0" {
					t.Fatal("fallback was not a tiny range")
				}
				res := response(status, "x", http.Header{"Etag": {`"v1"`}, "Content-Type": {"image/jpeg"}})
				if status == 206 {
					res.Header.Set("Content-Range", "bytes 0-0/9000")
				} else {
					res.ContentLength = 9000
				}
				return res, nil
			}), Limits{})
			meta, err := s.Metadata(context.Background(), source(t))
			if err != nil || meta.Size == nil || *meta.Size != 9000 || !meta.SizeExact || !meta.StrongETag || calls != 2 {
				t.Fatalf("metadata=%+v err=%v", meta, err)
			}
			if status == 206 && meta.Ranges != "verified" {
				t.Fatal("range observation missing")
			}
		})
	}
	res := response(200, "", http.Header{"Etag": {`W/"weak"`}})
	res.ContentLength = -1
	meta, err := serviceResponse(res).Metadata(context.Background(), source(t))
	if err != nil || meta.Size != nil || meta.SizeExact || meta.StrongETag || meta.ModifiedAt != nil || meta.Ranges != "unknown" {
		t.Fatalf("uncertain metadata lost: %+v %v", meta, err)
	}
}

func TestHEADFallbackDoesNotSendProbeBody(t *testing.T) {
	var calls int
	s := New(fetchFunc(func(_ context.Context, _ *upstream.Root, method, _ string, opts upstream.Options) (*http.Response, error) {
		calls++
		if method == "HEAD" {
			return response(501, "", nil), nil
		}
		if opts.Headers.Get("Range") != "bytes=0-0" {
			t.Fatal("wrong fallback range")
		}
		return response(206, "x", http.Header{"Content-Range": {"bytes 0-0/100"}}), nil
	}), Limits{})
	w := httptest.NewRecorder()
	if err := s.Serve(w, httptest.NewRequest("HEAD", "/", nil), source(t), false); err != nil || w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "100" || w.Header().Get("Content-Range") != "" || calls != 2 {
		t.Fatalf("HEAD fallback: %v", err)
	}
}

func TestEmptyMetadataStillChecksRepresentation(t *testing.T) {
	var calls int
	s := New(fetchFunc(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(405, "", nil), nil
		}
		return response(416, "", http.Header{"Content-Range": {"bytes */0"}, "Etag": {`"new"`}}), nil
	}), Limits{})
	src := source(t)
	src.ExpectedETag = `"old"`
	_, err := s.Metadata(context.Background(), src)
	errorCode(t, err, "representation_changed", 412)
}

func TestRangedPreviewPinsSniffedRepresentation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		var calls int
		probe := make([]byte, 512)
		copy(probe, []byte{0xff, 0xd8, 0xff, 0xdb})
		s := New(fetchFunc(func(_ context.Context, _ *upstream.Root, _, _ string, opts upstream.Options) (*http.Response, error) {
			calls++
			if calls == 1 {
				if opts.Headers.Get("Range") != "bytes=0-511" {
					t.Fatal("wrong sniff range")
				}
				return response(206, string(probe), http.Header{"Content-Range": {"bytes 0-511/1024"}, "Content-Type": {"image/jpeg"}, "Etag": {`"v1"`}}), nil
			}
			if opts.Headers.Get("If-Match") != `"v1"` {
				t.Fatal("sniffed representation was not pinned")
			}
			etag := `"v1"`
			if changed {
				etag = `"v2"`
			}
			return response(206, "data", http.Header{"Content-Range": {"bytes 512-515/1024"}, "Content-Type": {"image/jpeg"}, "Etag": {etag}}), nil
		}), Limits{})
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", "bytes=512-515")
		w := httptest.NewRecorder()
		err := s.Serve(w, r, source(t), true)
		if changed {
			errorCode(t, err, "representation_changed", 412)
			if w.Body.Len() != 0 {
				t.Fatal("changed preview committed")
			}
		} else if err != nil || w.Body.String() != "data" {
			t.Fatalf("ranged preview: %v", err)
		}
	}
}

type generatedBody struct {
	remaining int64
	maxRead   int
	closed    bool
}

func (b *generatedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	b.maxRead = max(b.maxRead, len(p))
	n := int(min(int64(len(p)), b.remaining))
	b.remaining -= int64(n)
	return n, nil
}
func (b *generatedBody) Close() error { b.closed = true; return nil }

type discardResponse struct {
	header  http.Header
	written int64
	status  int
}

func (w *discardResponse) Header() http.Header    { return w.header }
func (w *discardResponse) WriteHeader(status int) { w.status = status }
func (w *discardResponse) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	return len(p), nil
}

func TestGiBStreamUsesBoundedBuffers(t *testing.T) {
	body := &generatedBody{remaining: 1 << 30}
	res := response(200, "", nil)
	res.Body = body
	res.ContentLength = 1 << 30
	w := &discardResponse{header: make(http.Header)}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := serviceResponse(res).Serve(w, httptest.NewRequest("GET", "/", nil), source(t), false)
	runtime.ReadMemStats(&after)
	if err != nil || w.written != 1<<30 || body.maxRead > 32<<10 || !body.closed {
		t.Fatalf("stream bytes=%d read=%d closed=%v err=%v", w.written, body.maxRead, body.closed, err)
	}
	if after.TotalAlloc-before.TotalAlloc > 8<<20 {
		t.Fatalf("stream allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	}
}

func TestShortBodyAborts(t *testing.T) {
	res := response(200, "short", nil)
	res.ContentLength = 100
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("short body did not abort: %v", recovered)
		}
	}()
	_ = serviceResponse(res).Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), source(t), false)
}

type cancellableBody struct {
	ctx    context.Context
	reads  atomic.Int32
	closed chan struct{}
	once   atomic.Bool
}

func (b *cancellableBody) Read(p []byte) (int, error) {
	if b.reads.Add(1) <= 4 {
		return len(p), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *cancellableBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		close(b.closed)
	}
	return nil
}

func TestBrowserDisconnectCancelsSource(t *testing.T) {
	closed := make(chan struct{})
	s := New(fetchFunc(func(ctx context.Context, _ *upstream.Root, _, _ string, _ upstream.Options) (*http.Response, error) {
		res := response(200, "", nil)
		res.ContentLength = 1 << 30
		res.Body = &cancellableBody{ctx: ctx, closed: closed}
		return res, nil
	}), Limits{})
	src := source(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Serve(w, r, src, false); err != nil {
			http.Error(w, "failed", 500)
		}
	}))
	defer server.Close()
	res, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, res.Body, 64<<10); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("browser disconnect did not close upstream body")
	}
}

func TestSourceErrorsDoNotExposeBodies(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		w := httptest.NewRecorder()
		err := serviceResponse(response(status, "source secret", http.Header{"Www-Authenticate": {"Basic secret"}})).Serve(w, httptest.NewRequest("GET", "/", nil), source(t), false)
		if err == nil || strings.Contains(err.Error(), "secret") || w.Body.Len() != 0 || w.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("source error exposed: %v", err)
		}
	}
}
