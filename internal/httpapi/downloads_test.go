package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/testfixture"
)

func TestDownloadsHTTPWorkflow(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	files, err := downloadfs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue := downloads.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil, files, downloads.Limits{FreeReserveBytes: -1})
	t.Cleanup(func() { queue.Close(); files.Close() })
	f.server.Downloads = queue
	f.handler = f.server.Handler(http.NotFoundHandler())
	entry := findEntry(t, browsePage(t, f, profile.ID, cookie, "", nil), "file2.txt")
	body := fmt.Sprintf(`{"profile_id":%q,"entry_id":%q,"destination_id":"server"}`, profile.ID, entry.ID)
	headers := map[string]string{"Idempotency-Key": "http-download"}
	requireStatus(t, f.request("GET", "/api/v1/destinations", "", nil, "", nil), 401)
	w := f.request("GET", "/api/v1/destinations", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"id":"server"`) || strings.Contains(w.Body.String(), `:\`) {
		t.Fatal("unsafe destination response", w.Body.String())
	}
	requireStatus(t, f.request("POST", "/api/v1/downloads", body, cookie, session.CSRFToken, nil), 400)
	w = f.request("POST", "/api/v1/downloads", body, cookie, session.CSRFToken, headers)
	requireStatus(t, w, 202)
	original := w.Body.String()
	var item downloads.Download
	if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/downloads/" + item.ID
	requireStatus(t, f.request("POST", path+"/pause", "", cookie, session.CSRFToken, nil), 202)
	requireStatus(t, f.request("POST", path+"/resume", `{"allow_restart":true,"url":"https://invalid.example/"}`, cookie, session.CSRFToken, nil), 400)
	requireStatus(t, f.request("POST", path+"/resume", "", cookie, session.CSRFToken, nil), 202)
	if err = queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = f.request("GET", path, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.State == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if item.State != "completed" {
		t.Fatalf("queue failed: %+v", item)
	}
	w = f.request("POST", "/api/v1/downloads", body, cookie, session.CSRFToken, headers)
	requireStatus(t, w, 202)
	if w.Body.String() != original {
		t.Fatal("creation replay did not retain original response")
	}
	for _, query := range []string{"?state=bad", "?limit=0", "?state=running&state=completed", "?url=https://invalid.example/"} {
		requireStatus(t, f.request("GET", "/api/v1/downloads"+query, "", cookie, "", nil), 400)
	}
	requireStatus(t, f.request("GET", "/api/v1/downloads?state=completed", "", cookie, "", nil), 200)
	w = f.request("GET", path+"/content", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if w.Body.String() != "0123456789" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe completed response", w.Header())
	}
	etag := w.Header().Get("ETag")
	for _, tc := range []struct {
		method, rangeValue, expected string
		status                       int
	}{{"GET", "bytes=2-5", "2345", 206}, {"GET", "bytes=-3", "789", 206}, {"GET", "bytes=7-", "789", 206}, {"HEAD", "bytes=2-5", "", 206}, {"GET", "bytes=99-", "", 416}, {"GET", "bytes=0-0,2-2", "", 416}} {
		w = f.request(tc.method, path+"/content", "", cookie, "", map[string]string{"Range": tc.rangeValue})
		requireStatus(t, w, tc.status)
		if tc.status != 416 && w.Body.String() != tc.expected {
			t.Fatal("incorrect range body", w.Body.String())
		}
	}
	requireStatus(t, f.request("GET", path+"/content", "", cookie, "", map[string]string{"If-None-Match": etag}), 304)
	w = f.request("GET", path+"/content", "", cookie, "", map[string]string{"Range": "bytes=2-5", "If-Range": `"different"`})
	requireStatus(t, w, 200)
	if w.Body.String() != "0123456789" {
		t.Fatal("If-Range did not fall back")
	}
	_, token := f.createToken(t, cookie, session, profile.ID, []string{"downloads:write", "content:read"})
	bearer := map[string]string{"Authorization": "Bearer " + token}
	requireStatus(t, f.request("GET", path, "", nil, "", bearer), 404)
	requireStatus(t, f.request("GET", path+"/content", "", nil, "", bearer), 404)
	requireStatus(t, f.request("DELETE", path+"/content", "", nil, "", bearer), 403)
	requireStatus(t, f.request("DELETE", path+"/partial", "", cookie, session.CSRFToken, nil), 409)
	requireStatus(t, f.request("DELETE", path+"/content", "", cookie, session.CSRFToken, nil), 204)
	requireStatus(t, f.request("GET", path+"/content", "", cookie, "", nil), 404)
}

type revokedDownloadWriter struct {
	*httptest.ResponseRecorder
	revoke  func()
	stopped chan struct{}
	once    sync.Once
}

func (w *revokedDownloadWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.stopped) })
	}
	return nil
}
func (w *revokedDownloadWriter) Write(b []byte) (int, error) {
	w.revoke()
	select {
	case <-w.stopped:
		return 0, context.Canceled
	case <-time.After(4 * time.Second):
		return 0, fmt.Errorf("write cancellation deadline did not fire")
	}
}

func TestCompletedDownloadStopsWhenTokenRevoked(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	fetcher.handler = testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{"/root/file2.txt": {Data: []byte(strings.Repeat("synthetic", 10000))}}})
	entry := findEntry(t, browsePage(t, f, profile.ID, cookie, "", nil), "file2.txt")
	files, err := downloadfs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue := downloads.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil, files, downloads.Limits{FreeReserveBytes: -1})
	t.Cleanup(func() { queue.Close(); files.Close() })
	f.server.Downloads = queue
	f.handler = f.server.Handler(http.NotFoundHandler())
	if err = queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	token, raw := f.createToken(t, cookie, session, profile.ID, []string{"downloads:write", "content:read"})
	body := fmt.Sprintf(`{"profile_id":%q,"entry_id":%q,"destination_id":"server"}`, profile.ID, entry.ID)
	w := f.request("POST", "/api/v1/downloads", body, nil, "", map[string]string{"Authorization": "Bearer " + raw, "Idempotency-Key": "stream"})
	requireStatus(t, w, 202)
	var item downloads.Download
	if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/downloads/" + item.ID
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = f.request("GET", path, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.State == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if item.State != "completed" {
		t.Fatal("fixture did not complete")
	}
	request := httptest.NewRequest("GET", f.origin+path+"/content", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	output := &revokedDownloadWriter{ResponseRecorder: httptest.NewRecorder(), stopped: make(chan struct{}), revoke: func() {
		if err := f.server.Auth.DeleteToken(context.Background(), token.ID); err != nil {
			t.Error(err)
		}
	}}
	started := time.Now()
	var recovered any
	func() { defer func() { recovered = recover() }(); f.handler.ServeHTTP(output, request) }()
	if recovered != http.ErrAbortHandler || time.Since(started) > 3*time.Second {
		t.Fatalf("revoked stream did not abort promptly: %v, %s", recovered, time.Since(started))
	}
}
