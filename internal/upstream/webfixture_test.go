//go:build webfixture

package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebFixtureDialIsPinned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fixture") }))
	defer server.Close()
	client, err := NewWebFixtureClient(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	root, err := NewRoot("http://fixture.example.test/", true)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(context.Background(), root, "GET", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "fixture" {
		t.Fatalf("fixture response: %q %v", body, err)
	}
	for _, target := range []string{"http://other.example.test/", "http://fixture.example.test:81/", "http://127.0.0.1/", "http://93.184.216.34/", "http://93.184.216.35/"} {
		root, err := NewRoot(target, true)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(context.Background(), root, "GET", "", Options{})
		if err == nil {
			response.Body.Close()
			t.Fatalf("unpinned destination accepted: %s", target)
		}
	}
	for _, address := range []string{"example.test:80", "192.168.1.2:80", "127.0.0.1:0", "[::1]:80"} {
		if _, err := NewWebFixtureClient(address); err == nil {
			t.Fatalf("invalid fixture listener accepted: %s", address)
		}
	}
	production := New(Policy{})
	defer production.CloseIdleConnections()
	root, _ = NewRoot(server.URL, true)
	if response, err := production.Do(context.Background(), root, "GET", "", Options{}); err == nil {
		response.Body.Close()
		t.Fatal("production client accepted loopback")
	}
}
