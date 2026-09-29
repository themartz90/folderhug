package mediaworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testLease() lease {
	return lease{ID: "job_123", Token: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)), ExpiresAt: time.Now().Add(time.Minute), Size: 128, ETag: `"version1"`}
}
func testJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 16)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func contentClient(t *testing.T, job lease, data []byte, extra func(*http.Request, *http.Response)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "broker" || r.URL.Path != "/jobs/"+job.ID+"/content" || r.Header.Get("Authorization") != "Bearer "+job.Token || r.Header.Get("If-Range") != job.ETag {
			t.Errorf("invalid broker request")
		}
		start, end, err := requestedRange(r.Header.Get("Range"), job.Size)
		if err != nil {
			return nil, err
		}
		end = min(end, start+31, int64(len(data)-1))
		body := data[start : end+1]
		response := &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
		response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, job.Size))
		response.Header.Set("Content-Length", fmt.Sprint(len(body)))
		response.Header.Set("ETag", job.ETag)
		if extra != nil {
			extra(r, response)
		}
		return response, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestBridgeIsolationAndClampedRanges(t *testing.T) {
	job := testLease()
	data := bytes.Repeat([]byte{'x'}, int(job.Size))
	var calls atomic.Int64
	client := contentClient(t, job, data, func(r *http.Request, response *http.Response) {
		calls.Add(1)
		for _, name := range []string{"Cookie", "X-Leak", "Referer", "Proxy-Authorization"} {
			if r.Header.Get(name) != "" {
				t.Errorf("forwarded %s", name)
			}
		}
		response.Header.Set("Set-Cookie", "secret=value")
		response.Header.Set("Authorization", "secret")
		response.Header.Set("Location", "https://example.invalid/")
	})
	b, err := newBridge(context.Background(), client, job)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, method := range []string{"GET", "HEAD"} {
		r, _ := http.NewRequest(method, b.URL(), nil)
		r.Header.Set("Range", "bytes=4-100")
		r.Header.Set("Cookie", "private=value")
		r.Header.Set("Authorization", "Bearer wrong")
		r.Header.Set("X-Leak", "private")
		r.Header.Set("Referer", "https://private.invalid")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 206 || response.Header.Get("Content-Range") != "bytes 4-35/128" || method == "GET" && len(body) != 32 {
			t.Fatalf("clamped response: %d %q", response.StatusCode, body)
		}
		for _, name := range []string{"Set-Cookie", "Authorization", "Location"} {
			if response.Header.Get(name) != "" {
				t.Fatalf("leaked %s", name)
			}
		}
	}
	before := calls.Load()
	for _, tc := range []struct {
		method, suffix, host, rangeHeader string
		want                              int
	}{
		{"GET", "/other", "", "", 404}, {"GET", "?url=http://private.invalid", "", "", 404},
		{"GET", "", "attacker.invalid", "", 404}, {"POST", "", "", "", 405},
		{"GET", "", "", "bytes=1-2,3-4", 416}, {"GET", "", "", "bytes=999-", 416},
	} {
		r, _ := http.NewRequest(tc.method, b.URL()+tc.suffix, nil)
		r.Header.Set("Range", tc.rangeHeader)
		if tc.host != "" {
			r.Host = tc.host
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Errorf("%+v got %d", tc, response.StatusCode)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid bridge request reached broker")
	}
}

func TestBridgeRejectsInvalidResponses(t *testing.T) {
	for _, mutation := range []func(*http.Response){
		func(r *http.Response) { r.StatusCode = 302; r.Header.Set("Location", "https://example.invalid") },
		func(r *http.Response) { r.Header.Set("ETag", `"other"`) },
		func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") },
		func(r *http.Response) { r.Header.Set("Content-Range", "bytes 1-31/128") },
		func(r *http.Response) { r.Header.Set("Content-Length", "128") },
	} {
		job := testLease()
		b, err := newBridge(context.Background(), contentClient(t, job, make([]byte, job.Size), func(_ *http.Request, r *http.Response) { mutation(r) }), job)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Get(b.URL())
		if err != nil {
			b.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		b.Close()
		if response.StatusCode != 502 {
			t.Fatalf("invalid response allowed: %d", response.StatusCode)
		}
	}
}

func TestLeaseExpiryAndCancellation(t *testing.T) {
	job := testLease()
	job.ExpiresAt = time.Now().Add(-time.Second)
	w := worker{client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("expired job accessed broker")
		return nil, ErrBroker
	})}, render: func(context.Context, string, string) ([]byte, error) {
		t.Error("expired job rendered")
		return nil, nil
	}}
	w.process(context.Background(), job)
	job = testLease()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	b, err := newBridge(ctx, client, job)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	done := make(chan struct{})
	go func() {
		response, _ := http.Get(b.URL())
		if response != nil {
			response.Body.Close()
		}
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge request survived cancellation")
	}
}

func TestWorkerPipelineAndExpiryDeadline(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		job := testLease()
		if expiry {
			job.ExpiresAt = time.Now().Add(100 * time.Millisecond)
		}
		data := make([]byte, job.Size)
		copy(data[4:], "ftyp")
		content := contentClient(t, job, data, nil)
		var result []byte
		var contentType string
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/content") {
				return content.Do(r)
			}
			if r.URL.Path != "/jobs/"+job.ID+"/result" || r.Header.Get("Authorization") != "Bearer "+job.Token {
				t.Error("wrong result capability")
			}
			result, _ = io.ReadAll(r.Body)
			contentType = r.Header.Get("Content-Type")
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		w := worker{client: client, render: func(ctx context.Context, input, format string) ([]byte, error) {
			if strings.Contains(input, job.Token) || !strings.HasPrefix(input, "http://127.0.0.1:") || format != "mov" {
				t.Error("renderer received invalid input")
			}
			if expiry {
				<-ctx.Done()
				return nil, errors.New("sensitive FFmpeg stderr")
			}
			return testJPEG(t), nil
		}}
		w.process(context.Background(), job)
		if expiry {
			if string(result) != `{"state":"unavailable"}` || contentType != "application/json" {
				t.Fatalf("unsafe failure result %q", result)
			}
		} else if !validJPEG(result) || contentType != "image/jpeg" {
			t.Fatal("missing poster")
		}
	}
}

func TestClaimValidationAndPollingCancellation(t *testing.T) {
	job := testLease()
	body, _ := json.Marshal(job)
	for _, bad := range []string{strings.Replace(string(body), "job_123", "../escape", 1), strings.Replace(string(body), "job_123", "bad%2fid", 1), string(body) + `{}`, strings.Repeat("x", 4097)} {
		w := worker{key: "workerkey", client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "Bearer workerkey" {
				t.Error("claim missing worker key")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(bad))}, nil
		})}}
		if _, err := w.claim(context.Background()); !errors.Is(err, ErrProtocol) {
			t.Errorf("invalid claim accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := worker{poll: time.Hour, client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
	if err := w.run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUnixTransportIgnoresProxyAndRejectsRedirect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket deployment test")
	}
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	t.Setenv("ALL_PROXY", target.URL)
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
	})}
	go server.Serve(listener)
	defer server.Close()
	client, transport := unixClient(socket)
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequest("POST", "http://unrelated.invalid/claim", nil)
	request.Header.Set("Authorization", "Bearer private")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 302 || leaked.Load() != 0 {
		t.Fatal("redirect or proxy escaped socket")
	}
}

func TestConfigRejectsWrongKeyLength(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	for _, length := range []int{0, 31, 33} {
		if err := os.WriteFile(key, make([]byte, length), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Run(context.Background(), Config{SocketPath: filepath.Join(dir, "socket"), KeyPath: key}); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid key accepted")
		}
	}
}

func TestBridgeAggregateBudgetAndFormatRejection(t *testing.T) {
	job := testLease()
	b, err := newBridge(context.Background(), contentClient(t, job, make([]byte, job.Size), nil), job)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.format(context.Background()); err == nil {
		t.Fatal("unknown media accepted")
	}
	b.bytes.Store(MaxInputBytes - 1)
	request := httptest.NewRequest("GET", b.URL(), nil)
	request.Host = b.listener.Addr().String()
	recorder := httptest.NewRecorder()
	b.ServeHTTP(recorder, request)
	if recorder.Code != 429 || b.ctx.Err() == nil {
		t.Fatal("aggregate input limit did not cancel bridge")
	}
}
