package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
)

func TestMountedDownloadOwnerRoutesAndTokenBoundary(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	files, err := downloadfs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Movies"), 0700); err != nil {
		t.Fatal(err)
	}
	queue := downloads.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil, files, downloads.Limits{FreeReserveBytes: -1})
	queue.ConfigureMounts([]downloads.MountedRoot{{ID: "media", Label: "Media", Path: root}})
	t.Cleanup(func() { queue.Close(); files.Close() })
	f.server.Downloads = queue
	f.handler = f.server.Handler(http.NotFoundHandler())
	entry := findEntry(t, browsePage(t, f, profile.ID, cookie, "", nil), "file2.txt")
	_, raw := f.createToken(t, cookie, session, profile.ID, []string{"downloads:write", "content:read"})
	bearer := map[string]string{"Authorization": "Bearer " + raw}
	w := f.request("GET", "/api/v1/destinations", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"id":"media"`) || strings.Contains(w.Body.String(), root) {
		t.Fatal("owner destination response", w.Body.String())
	}
	w = f.request("GET", "/api/v1/destinations", "", nil, "", bearer)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), `"id":"media"`) {
		t.Fatal("token saw mounted destination")
	}
	w = f.request("GET", "/api/v1/destinations/media/folders", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"path":"Movies"`) {
		t.Fatal("folder missing", w.Body.String())
	}
	requireStatus(t, f.request("GET", "/api/v1/destinations/media/folders", "", nil, "", bearer), 403)
	bad := fmt.Sprintf(`{"profile_id":%q,"entry_id":%q,"destination_id":"media","destination_path":"../outside"}`, profile.ID, entry.ID)
	requireStatus(t, f.request("POST", "/api/v1/downloads", bad, cookie, session.CSRFToken, map[string]string{"Idempotency-Key": "bad-mount"}), 400)
	body := fmt.Sprintf(`{"profile_id":%q,"entry_id":%q,"destination_id":"media","destination_path":"Movies"}`, profile.ID, entry.ID)
	requireStatus(t, f.request("POST", "/api/v1/downloads", body, nil, "", map[string]string{"Authorization": "Bearer " + raw, "Idempotency-Key": "token-mount"}), 404)
	w = f.request("POST", "/api/v1/downloads", body, cookie, session.CSRFToken, map[string]string{"Idempotency-Key": "owner-mount"})
	requireStatus(t, w, 202)
	var job downloads.Download
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.DestinationPath != "Movies" {
		t.Fatal("destination lost", job)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = f.request("GET", "/api/v1/downloads/"+job.ID, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.State != "completed" || job.SavedName == nil {
		t.Fatal("mounted job did not complete", job)
	}
	path := "/api/v1/downloads/" + job.ID + "/content"
	w = f.request("GET", path, "", cookie, "", nil)
	requireStatus(t, w, 200)
	if w.Body.String() != "0123456789" {
		t.Fatal("mounted content mismatch", w.Body.String())
	}
	requireStatus(t, f.request("DELETE", path, "", cookie, session.CSRFToken, nil), 409)
	if _, err := os.Stat(filepath.Join(root, "Movies", *job.SavedName)); err != nil {
		t.Fatal("mounted file removed", err)
	}
}
