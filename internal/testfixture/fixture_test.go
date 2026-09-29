package testfixture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, client *http.Client, method, target string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

func TestRangeAndHead(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Files: map[string]File{"/large.bin": DeterministicFile(1 << 30)}}))
	defer server.Close()
	res, body := request(t, server.Client(), "HEAD", server.URL+"/large.bin", nil)
	if res.StatusCode != 200 || res.ContentLength != 1<<30 || len(body) != 0 {
		t.Fatalf("HEAD: status=%d length=%d body=%d", res.StatusCode, res.ContentLength, len(body))
	}
	etag := res.Header.Get("ETag")
	for _, tc := range []struct {
		name, value, contentRange string
		status                    int
		start                     int64
		size                      int
	}{
		{"first", "bytes=0-0", "bytes 0-0/1073741824", 206, 0, 1},
		{"offset", "bytes=1000000000-1000000003", "bytes 1000000000-1000000003/1073741824", 206, 1000000000, 4},
		{"suffix", "bytes=-3", "bytes 1073741821-1073741823/1073741824", 206, 1073741821, 3},
		{"unsatisfied", "bytes=1073741824-", "bytes */1073741824", 416, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, body := request(t, server.Client(), "GET", server.URL+"/large.bin", map[string]string{"Range": tc.value, "If-Range": etag})
			if res.StatusCode != tc.status || res.Header.Get("Content-Range") != tc.contentRange {
				t.Fatalf("status=%d range=%q", res.StatusCode, res.Header.Get("Content-Range"))
			}
			if tc.status != 206 {
				return
			}
			if len(body) != tc.size {
				t.Fatalf("body size=%d", len(body))
			}
			for i, b := range body {
				if b != byte((tc.start+int64(i))%251) {
					t.Fatalf("unexpected byte at %d", i)
				}
			}
		})
	}
}

func TestDeterministicContentAndConditions(t *testing.T) {
	server := httptest.NewServer(Handler(Config{}))
	defer server.Close()
	res, first := request(t, server.Client(), "GET", server.URL+"/sample.bin", nil)
	_, second := request(t, server.Client(), "GET", server.URL+"/sample.bin", nil)
	if !bytes.Equal(first, second) || len(first) != 65536 {
		t.Fatal("content is not deterministic")
	}
	conditional, body := request(t, server.Client(), "GET", server.URL+"/sample.bin", map[string]string{"If-None-Match": res.Header.Get("ETag")})
	if conditional.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatal("conditional GET failed")
	}
	stale, _ := request(t, server.Client(), "GET", server.URL+"/sample.bin", map[string]string{"Range": "bytes=0-1", "If-Range": `"stale"`})
	if stale.StatusCode != http.StatusOK {
		t.Fatal("stale If-Range must send a full representation")
	}
}

func TestListingFormatsAndEscapedNames(t *testing.T) {
	for _, format := range []Format{ApacheTable, ApachePre, Nginx} {
		t.Run(string(format), func(t *testing.T) {
			server := httptest.NewServer(Handler(Config{Format: format}))
			defer server.Close()
			res, body := request(t, server.Client(), "GET", server.URL+"/nested/", nil)
			if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
				t.Fatal("missing HTML listing")
			}
			for _, expected := range []string{`href="../"`, `href="deeper/"`, `href="hello%20world.txt"`, `café &amp; snow 雪.txt`} {
				if !strings.Contains(string(body), expected) {
					t.Errorf("missing %s", expected)
				}
			}
			_, deep := request(t, server.Client(), "GET", server.URL+"/nested/deeper/", nil)
			if !strings.Contains(string(deep), `href="100%25%20%23what%3F.txt"`) {
				t.Fatalf("reserved characters: %s", deep)
			}
			file, content := request(t, server.Client(), "GET", server.URL+"/nested/deeper/100%25%20%23what%3F.txt", nil)
			if file.StatusCode != 200 || string(content) != "Reserved URL characters.\n" {
				t.Fatal("escaped link does not resolve")
			}
		})
	}
}

func TestAuthAndReadOnly(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Username: "fixture", Password: "fixture"}))
	defer server.Close()
	res, _ := request(t, server.Client(), "GET", server.URL+"/", nil)
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("authentication not required")
	}
	req, _ := http.NewRequest("GET", server.URL+"/", nil)
	req.SetBasicAuth("fixture", "wrong")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("wrong synthetic credentials accepted")
	}
	req.SetBasicAuth("fixture", "fixture")
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("valid synthetic credentials rejected")
	}
	req.Method = "POST"
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 {
		t.Fatal("write method accepted")
	}
}

func TestFaultBehaviors(t *testing.T) {
	server := httptest.NewServer(Handler(Config{RetryAfter: 1100 * time.Millisecond}))
	defer server.Close()
	for _, tc := range []struct {
		behavior Behavior
		method   string
		status   int
	}{
		{IgnoredRange, "GET", 200}, {MalformedRange, "GET", 206}, {MissingHEAD, "HEAD", 405},
		{MissingHEAD, "GET", 206}, {Throttled, "GET", 429},
	} {
		t.Run(string(tc.behavior)+tc.method, func(t *testing.T) {
			res, body := request(t, server.Client(), tc.method, server.URL+"/_fixture/"+string(tc.behavior)+"/sample.bin", map[string]string{"Range": "bytes=0-3"})
			if res.StatusCode != tc.status {
				t.Fatalf("status=%d", res.StatusCode)
			}
			if tc.behavior == IgnoredRange && len(body) != 65536 {
				t.Fatal("range was not ignored")
			}
			if tc.behavior == MalformedRange && res.Header.Get("Content-Range") != "bytes invalid" {
				t.Fatal("malformed range missing")
			}
			if tc.behavior == Throttled && res.Header.Get("Retry-After") != "2" {
				t.Fatal("retry delay must round up")
			}
		})
	}
	first, a := request(t, server.Client(), "GET", server.URL+"/sample.bin?behavior=changed-etag", nil)
	second, b := request(t, server.Client(), "GET", server.URL+"/sample.bin?behavior=changed-etag", nil)
	if first.Header.Get("ETag") == second.Header.Get("ETag") || bytes.Equal(a, b) {
		t.Fatal("representation did not change")
	}
}

func TestInterruptedBody(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Behavior: Interrupted, InterruptAfter: 128}))
	defer server.Close()
	res, err := server.Client().Get(server.URL + "/sample.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(body) != 128 || res.ContentLength != 65536 {
		t.Fatalf("length=%d advertised=%d err=%v", len(body), res.ContentLength, err)
	}
}

func TestSlowStreamCancellation(t *testing.T) {
	server := httptest.NewServer(Handler(Config{Behavior: Slow, ChunkDelay: time.Second}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/sample.bin", nil)
	started := time.Now()
	res, err := server.Client().Do(req)
	if err == nil {
		defer res.Body.Close()
		_, err = io.ReadAll(res.Body)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("slow read ignored cancellation")
	}
}

func TestRedirectAndFormatOverride(t *testing.T) {
	server := httptest.NewServer(Handler(Config{RedirectTo: "/nested/"}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, target := range []string{"/nested", "/sample.bin?behavior=redirect"} {
		res, _ := request(t, client, "GET", server.URL+target, nil)
		if res.StatusCode < 300 || res.StatusCode >= 400 || res.Header.Get("Location") != "/nested/" {
			t.Fatal("expected directory redirect")
		}
	}
	_, body := request(t, client, "GET", server.URL+"/?format=nginx", nil)
	if !strings.Contains(string(body), "<pre>") || strings.Contains(string(body), "<table>") {
		t.Fatal("format override failed")
	}
	res, _ := request(t, client, "GET", server.URL+"/?behavior=unknown", nil)
	if res.StatusCode != 400 {
		t.Fatal("unknown behavior should fail explicitly")
	}
}
