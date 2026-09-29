package upstream

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/testfixture"
)

// The production address checks still see a public address. Only this test
// dial function maps that already checked address to the local fixture.
func fixtureClient(t *testing.T, server *httptest.Server, policy Policy) *Client {
	t.Helper()
	c := New(policy)
	c.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	c.dialIP = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:80" && address != "93.184.216.34:443" {
			t.Errorf("unchecked dial target %q", address)
			return nil, ErrPolicy
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func testRoot(t *testing.T, raw string) *Root {
	t.Helper()
	root, err := NewRoot(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddressPolicy(t *testing.T) {
	c := New(Policy{Allow: []Allowance{{Host: "private.example", Port: 8443, CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}}}})
	for _, address := range []string{
		"127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::", "169.254.169.254", "fe80::1", "ff02::1", "224.0.0.1", "fec0::1",
		"100.100.100.200", "168.63.129.16", "fd00:ec2::254", "192.0.2.1", "198.18.0.1", "240.0.0.1", "2002:7f00:1::", "64:ff9b::7f00:1", "2001:db8::1",
	} {
		t.Run(address, func(t *testing.T) {
			if c.addressAllowed("private.example", 8443, netip.MustParseAddr(address)) {
				t.Fatal("permanently forbidden address accepted")
			}
		})
	}
	for _, tc := range []struct {
		host    string
		port    uint16
		addr    string
		allowed bool
	}{
		{"public.example", 443, "93.184.216.34", true}, {"public.example", 80, "2606:4700:4700::1111", true},
		{"public.example", 443, "::ffff:93.184.216.34", true}, {"public.example", 8443, "93.184.216.34", false},
		{"private.example", 8443, "10.2.3.4", true}, {"private.example", 8443, "fd12::1", true},
		{"wrong.example", 8443, "10.2.3.4", false}, {"private.example", 443, "10.2.3.4", false},
	} {
		if got := c.addressAllowed(tc.host, tc.port, netip.MustParseAddr(tc.addr)); got != tc.allowed {
			t.Errorf("%s:%d/%s allowed=%v", tc.host, tc.port, tc.addr, got)
		}
	}
}

func TestRejectedDNSNeverDials(t *testing.T) {
	for _, answer := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("::ffff:169.254.169.254")},
		{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")},
	} {
		c := New(Policy{})
		c.lookupIP = func(context.Context, string) ([]netip.Addr, error) { return answer, nil }
		var dials atomic.Int32
		c.dialIP = func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, ErrNetwork }
		_, err := c.Do(context.Background(), testRoot(t, "http://source.example/"), "GET", "", Options{})
		if !errors.Is(err, ErrPolicy) || dials.Load() != 0 {
			t.Fatalf("err=%v dials=%d", err, dials.Load())
		}
	}
}

func TestRebindingOnFreshConnection(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	var lookups atomic.Int32
	c.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	root := testRoot(t, "http://source.example/")
	res, err := c.Do(context.Background(), root, "GET", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if readBody(t, res) != "ok" {
		t.Fatal("first representation missing")
	}
	c.CloseIdleConnections()
	_, err = c.Do(context.Background(), root, "GET", "", Options{})
	if !errors.Is(err, ErrPolicy) || hits.Load() != 1 {
		t.Fatalf("rebound address reached: err=%v hits=%d", err, hits.Load())
	}
}

func TestRedirectConfinementAndCredentials(t *testing.T) {
	var forbiddenHits atomic.Int32
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forbiddenHits.Add(1) }))
	defer forbidden.Close()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture" || password != "test-password" {
			t.Error("source Basic credentials missing")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unsafe request header forwarded")
		}
		switch r.URL.Path {
		case "/files":
			http.Redirect(w, r, "/files/", 301)
		case "/files/":
			io.WriteString(w, "listing")
		case "/files/escape":
			http.Redirect(w, r, forbidden.URL+"/", 302)
		case "/files/outside":
			http.Redirect(w, r, "/files-other/", 302)
		case "/files/loop":
			http.Redirect(w, r, "/files/loop", 302)
		default:
			t.Error("forbidden path reached")
		}
	}))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	root := testRoot(t, "http://source.example/files")
	opts := Options{Credentials: &Credentials{Username: "fixture", Password: "test-password"}, Headers: http.Header{"Cookie": {"secret"}, "Proxy-Authorization": {"secret"}, "Authorization": {"Bearer ignored"}}}
	res, err := c.Do(context.Background(), root, "GET", "", opts)
	if err != nil || readBody(t, res) != "listing" {
		t.Fatalf("canonical redirect failed: %v", err)
	}
	for _, name := range []string{"escape", "outside"} {
		if _, err := c.Do(context.Background(), root, "GET", name, opts); !errors.Is(err, ErrURL) {
			t.Fatalf("redirect %s: %v", name, err)
		}
	}
	before := hits.Load()
	if _, err := c.Do(context.Background(), root, "GET", "loop", opts); !errors.Is(err, ErrRedirect) {
		t.Fatalf("loop: %v", err)
	}
	if hits.Load()-before != 4 || forbiddenHits.Load() != 0 {
		t.Fatal("redirect limit or origin check failed")
	}
}

func TestTLSHostnameAndUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.TLS.ServerName) }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	root := testRoot(t, "https://example.com/")
	if _, err := c.Do(context.Background(), root, "GET", "", Options{}); !errors.Is(err, ErrNetwork) {
		t.Fatalf("untrusted certificate accepted: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	c.transport.TLSClientConfig.RootCAs = pool
	res, err := c.Do(context.Background(), root, "GET", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if readBody(t, res) != "example.com" {
		t.Fatal("TLS used dial IP instead of source hostname")
	}
	if _, err := c.Do(context.Background(), testRoot(t, "https://wrong.example/"), "GET", "", Options{}); !errors.Is(err, ErrNetwork) {
		t.Fatalf("wrong hostname accepted: %v", err)
	}
}

func TestFixtureRangeAndByteBudgets(t *testing.T) {
	server := httptest.NewServer(testfixture.Handler(testfixture.Config{}))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	root := testRoot(t, "http://source.example/")
	res, err := c.Do(context.Background(), root, "GET", "sample.bin", Options{Headers: http.Header{"Range": {"bytes=10-13"}}, MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 206 || res.Header.Get("Content-Range") != "bytes 10-13/65536" || readBody(t, res) != string([]byte{10, 11, 12, 13}) {
		t.Fatal("range response changed")
	}
	if _, err := c.Do(context.Background(), root, "GET", "sample.bin", Options{MaxBytes: 4}); !errors.Is(err, ErrResponseLimit) {
		t.Fatalf("declared body limit: %v", err)
	}
	res, err = c.Do(context.Background(), root, "HEAD", "sample.bin", Options{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentLength != 65536 || readBody(t, res) != "" {
		t.Fatal("HEAD must keep metadata without reading a body")
	}
}

func TestUnknownSizeByteBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); io.WriteString(w, "12345") }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	res, err := c.Do(context.Background(), testRoot(t, "http://source.example/"), "GET", "", Options{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "1234" || !errors.Is(err, ErrResponseLimit) {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestCancellationAndReservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "body") }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{MaxConcurrent: 2, PerOrigin: 2})
	root := testRoot(t, "http://source.example/")
	first, err := c.Do(context.Background(), root, "GET", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if _, err := c.Do(context.Background(), root, "GET", "", Options{Timeout: 20 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("background used reserved slot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	interactive, err := c.Do(ctx, root, "GET", "", Options{Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer interactive.Body.Close()
	if _, err := c.Do(context.Background(), root, "GET", "", Options{Interactive: true, Timeout: 20 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrency limit exceeded: %v", err)
	}
	cancel()
	// Cancellation releases the slot even when the caller has not closed yet.
	res, err := c.Do(context.Background(), root, "GET", "", Options{Interactive: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	first.Body.Close()
	res, err = c.Do(context.Background(), root, "GET", "", Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

func TestGlobalBudgetAcrossOrigins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "body") }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{MaxConcurrent: 2, PerOrigin: 2})
	first, err := c.Do(context.Background(), testRoot(t, "http://first.example/"), "GET", "", Options{Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	second, err := c.Do(context.Background(), testRoot(t, "http://second.example/"), "GET", "", Options{Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	_, err = c.Do(context.Background(), testRoot(t, "http://third.example/"), "GET", "", Options{Interactive: true, Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global budget was not shared: %v", err)
	}
}

func TestRequestCancellationBeforeHeaders(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := c.Do(ctx, testRoot(t, "http://source.example/"), "GET", "", Options{})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach fixture")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation")
	}
}

func TestTimeoutErrorsAreRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := fixtureClient(t, server, Policy{HeaderTimeout: 20 * time.Millisecond})
	_, err := c.Do(context.Background(), testRoot(t, "http://source.example/private-folder/"), "GET", "private-file", Options{Credentials: &Credentials{Username: "private-user", Password: "private-password"}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("header timeout: %v", err)
	}
	for _, private := range []string{"source.example", "private-folder", "private-file", "private-user", "private-password"} {
		if strings.Contains(err.Error(), private) {
			t.Fatal("private data in transport error")
		}
	}
	if c.transport.Proxy != nil {
		t.Fatal("environment proxy handling enabled")
	}
}
