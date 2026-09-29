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

type completedDownloadFixture struct {
	*authFixture
	cookie                  *http.Cookie
	profileID, tokenID, raw string
	job                     downloads.Download
	path, payload           string
}

func newCompletedDownloadFixture(t *testing.T) completedDownloadFixture {
	t.Helper()
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	payload := strings.Repeat("synthetic", 10000)
	fetcher.handler = testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{"/root/file2.txt": {Data: []byte(payload)}}})
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
	w := f.request("POST", "/api/v1/downloads", body, nil, "", map[string]string{"Authorization": "Bearer " + raw, "Idempotency-Key": "stream-authority"})
	requireStatus(t, w, 202)
	var item downloads.Download
	if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	jobPath := "/api/v1/downloads/" + item.ID
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = f.request("GET", jobPath, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.State == "completed" {
			return completedDownloadFixture{f, cookie, profile.ID, token.ID, raw, item, jobPath + "/content", payload}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fixture download did not complete")
	return completedDownloadFixture{}
}

type authorityDownloadWriter struct {
	*revokedDownloadWriter
	hookOnce   sync.Once
	blocked    bool
	firstChunk int
}

func (w *authorityDownloadWriter) Write(b []byte) (int, error) {
	w.hookOnce.Do(func() {
		w.firstChunk = len(b)
		w.revoke()
		if !w.blocked {
			// This write was already in flight when access changed. Later reads
			// must stop even if the writer ignores the cancellation deadline.
			time.Sleep(1500 * time.Millisecond)
		}
	})
	if w.blocked {
		select {
		case <-w.stopped:
			return 0, context.Canceled
		case <-time.After(4 * time.Second):
			return 0, fmt.Errorf("write cancellation deadline did not fire")
		}
	}
	return w.ResponseRecorder.Write(b)
}

func TestCompletedDownloadRechecksCurrentAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked bool
		change  func(completedDownloadFixture) error
		status  int
	}{
		{"profile_deleted_writable", false, func(f completedDownloadFixture) error {
			return f.server.Profiles.Delete(context.Background(), f.profileID)
		}, 404},
		{"profile_deleted_blocked", true, func(f completedDownloadFixture) error {
			return f.server.Profiles.Delete(context.Background(), f.profileID)
		}, 404},
		{"token_revoked", true, func(f completedDownloadFixture) error {
			return f.server.Auth.DeleteToken(context.Background(), f.tokenID)
		}, 401},
		{"token_expired", true, func(f completedDownloadFixture) error {
			_, err := f.server.Store.DB.Exec(`UPDATE api_tokens SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), f.tokenID)
			return err
		}, 401},
		{"content_scope_removed", true, func(f completedDownloadFixture) error {
			_, err := f.server.Store.DB.Exec(`UPDATE api_tokens SET scopes='["downloads:write"]' WHERE id=?`, f.tokenID)
			return err
		}, 403},
		{"profile_scope_removed", true, func(f completedDownloadFixture) error {
			_, err := f.server.Store.DB.Exec(`UPDATE api_tokens SET profile_ids='[]' WHERE id=?`, f.tokenID)
			return err
		}, 404},
		{"job_creator_changed", true, func(f completedDownloadFixture) error {
			_, err := f.server.Store.DB.Exec(`UPDATE download_jobs SET creator_id='owner',owner=1 WHERE id=?`, f.job.ID)
			return err
		}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompletedDownloadFixture(t)
			r := httptest.NewRequest("GET", f.origin+f.path, nil)
			r.Header.Set("Authorization", "Bearer "+f.raw)
			output := &authorityDownloadWriter{blocked: tc.blocked, revokedDownloadWriter: &revokedDownloadWriter{ResponseRecorder: httptest.NewRecorder(), stopped: make(chan struct{}), revoke: func() {
				if err := tc.change(f); err != nil {
					t.Error(err)
				}
			}}}
			started := time.Now()
			var recovered any
			func() { defer func() { recovered = recover() }(); f.handler.ServeHTTP(output, r) }()
			if recovered != http.ErrAbortHandler || time.Since(started) > 3*time.Second {
				t.Fatalf("stream did not abort promptly: panic=%v, duration=%s, bytes=%d", recovered, time.Since(started), output.Body.Len())
			}
			if output.firstChunk == 0 || output.Body.Len() > output.firstChunk || output.Body.Len() == len(f.payload) {
				t.Fatalf("stream continued beyond its in-flight write: first=%d, written=%d", output.firstChunk, output.Body.Len())
			}
			if tc.blocked && output.Body.Len() != 0 {
				t.Fatal("blocked writer received content after cancellation")
			}
			requireStatus(t, f.request("GET", f.path, "", nil, "", map[string]string{"Authorization": "Bearer " + f.raw}), tc.status)
			owner := f.request("GET", f.path, "", f.cookie, "", nil)
			requireStatus(t, owner, 200)
			if owner.Body.String() != f.payload {
				t.Fatal("owner lost access to completed file")
			}
		})
	}
}

func TestCompletedDownloadOwnerStreamSurvivesProfileDeletion(t *testing.T) {
	f := newCompletedDownloadFixture(t)
	r := httptest.NewRequest("GET", f.origin+f.path, nil)
	r.AddCookie(f.cookie)
	output := &authorityDownloadWriter{revokedDownloadWriter: &revokedDownloadWriter{ResponseRecorder: httptest.NewRecorder(), stopped: make(chan struct{}), revoke: func() {
		if err := f.server.Profiles.Delete(context.Background(), f.profileID); err != nil {
			t.Error(err)
		}
	}}}
	f.handler.ServeHTTP(output, r)
	if output.Code != 200 || output.Body.String() != f.payload {
		t.Fatal("owner stream was interrupted by profile removal")
	}
	requireStatus(t, f.request("GET", f.path, "", nil, "", map[string]string{"Authorization": "Bearer " + f.raw}), 404)
}
