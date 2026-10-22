package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/scans"
	"github.com/themartz90/folderhug/internal/testfixture"
)

func TestSearchHTTPReportsPartialAncestorCoverage(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	listing := testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{
		"/root/a/known.txt": {Data: []byte("indexed")},
		"/root/b/other.txt": {Data: []byte("unavailable")},
	}})
	fetcher.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/root/b/" {
			http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
			return
		}
		listing.ServeHTTP(w, r)
	})
	s := scans.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil)
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f.server.Scans = s
	f.handler = f.server.Handler(http.NotFoundHandler())
	directory := findEntry(t, browsePage(t, f, profile.ID, cookie, "", nil), "a")
	w := f.request("POST", "/api/v1/profiles/"+profile.ID+"/scans", `{}`, cookie, session.CSRFToken, map[string]string{"Idempotency-Key": "partial-ancestor-http"})
	requireStatus(t, w, 202)
	var scan scans.Scan
	if err := json.Unmarshal(w.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); scan.State != "partial" && time.Now().Before(deadline); {
		w = f.request("GET", "/api/v1/scans/"+scan.ID, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &scan); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if scan.State != "partial" || scan.DirectoriesFailed != 1 {
		t.Fatal("fixture did not finish with one failed sibling", scan)
	}
	w = f.request("GET", "/api/v1/search?profile_id="+profile.ID+"&directory_id="+directory.ID+"&q=known.txt", "", cookie, "", nil)
	requireStatus(t, w, 200)
	var page scans.SearchPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "known.txt" || len(page.Coverage) != 1 || page.Coverage[0].State != "partial" || page.Coverage[0].LastScanAt == nil || !page.Coverage[0].LastScanAt.Equal(scan.UpdatedAt) {
		t.Fatal("API lost partial coverage or its scan date", page)
	}
}

func TestSearchHTTPLongCursorSurvivesRescan(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	longName := "a" + strings.Repeat("1b", 1300) + ".txt"
	fetcher.handler = testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{
		"/root/" + longName: {Data: []byte("first")},
		"/root/z.txt":       {Data: []byte("last")},
	}})
	s := scans.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil)
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f.server.Scans = s
	f.handler = f.server.Handler(http.NotFoundHandler())
	var cursor string
	for _, key := range []string{"long-cursor-initial", "long-cursor-rescan"} {
		w := f.request("POST", "/api/v1/profiles/"+profile.ID+"/scans", `{}`, cookie, session.CSRFToken, map[string]string{"Idempotency-Key": key})
		requireStatus(t, w, 202)
		var scan scans.Scan
		if err := json.Unmarshal(w.Body.Bytes(), &scan); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); scan.State != "completed" && time.Now().Before(deadline); {
			w = f.request("GET", "/api/v1/scans/"+scan.ID, "", cookie, "", nil)
			requireStatus(t, w, 200)
			if err := json.Unmarshal(w.Body.Bytes(), &scan); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if scan.State != "completed" {
			t.Fatalf("fixture scan ended in %s", scan.State)
		}
		if key == "long-cursor-initial" {
			w = f.request("GET", "/api/v1/search?profile_id="+profile.ID+"&limit=1", "", cookie, "", nil)
			requireStatus(t, w, 200)
			var first scans.SearchPage
			if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
				t.Fatal(err)
			}
			if len(first.Entries) != 1 || first.Entries[0].Name != longName || first.NextCursor == nil || len(*first.NextCursor) <= 4096 {
				t.Fatal("fixture did not produce a long search cursor")
			}
			cursor = *first.NextCursor
		}
	}
	w := f.request("GET", "/api/v1/search?profile_id="+profile.ID+"&limit=1&cursor="+url.QueryEscape(cursor), "", cookie, "", nil)
	requireStatus(t, w, 200)
	var next scans.SearchPage
	if err := json.Unmarshal(w.Body.Bytes(), &next); err != nil || len(next.Entries) != 1 || next.Entries[0].Name != "z.txt" || next.NextCursor != nil {
		t.Fatalf("long cursor did not continue after rescan: %+v, %v", next, err)
	}
	requireStatus(t, f.request("GET", "/api/v1/search?cursor=a&cursor=b", "", cookie, "", nil), 400)
	requireStatus(t, f.request("GET", "/api/v1/search?cursor="+strings.Repeat("x", scans.MaxSearchCursorLength+1), "", cookie, "", nil), 409)
}

func TestScanSearchAndExportHTTPWorkflow(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	s := scans.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, fetcher, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f.server.Scans = s
	f.handler = f.server.Handler(http.NotFoundHandler())
	path := "/api/v1/profiles/" + profile.ID + "/scans"
	headers := map[string]string{"Idempotency-Key": "scan-http-fixture"}
	requireStatus(t, f.request("POST", path, `{}`, cookie, session.CSRFToken, nil), 400)
	response := f.request("POST", path, `{}`, cookie, session.CSRFToken, headers)
	requireStatus(t, response, 202)
	var first scans.Scan
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	replayed := f.request("POST", path, `{}`, cookie, session.CSRFToken, headers)
	requireStatus(t, replayed, 202)
	var replay scans.Scan
	if err := json.Unmarshal(replayed.Body.Bytes(), &replay); err != nil || first.ID != replay.ID {
		t.Fatal("idempotent creation duplicated a job")
	}
	requireStatus(t, f.request("POST", path, `{"max_depth":0}`, cookie, session.CSRFToken, headers), 409)
	var completed scans.Scan
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w := f.request("GET", "/api/v1/scans/"+first.ID, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &completed); err != nil {
			t.Fatal(err)
		}
		if completed.State == "completed" {
			break
		}
		if completed.State == "failed" || completed.State == "partial" {
			t.Fatalf("scan ended: %+v", completed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.State != "completed" || completed.DirectoriesVisited != 2 || completed.FilesFound != 5 {
		t.Fatalf("scan did not cover fixture: %+v", completed)
	}
	search := f.request("GET", "/api/v1/search?q=file&profile_id="+profile.ID+"&limit=1", "", cookie, "", nil)
	requireStatus(t, search, 200)
	var page scans.SearchPage
	if err := json.Unmarshal(search.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "file2.txt" || page.NextCursor == nil || len(page.Coverage) != 1 || page.Coverage[0].State != "complete" {
		t.Fatal("incorrect indexed page", page)
	}
	requireStatus(t, f.request("GET", "/api/v1/search?q=file&profile_id="+profile.ID+"&limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", cookie, "", nil), 200)
	omitted := f.request("GET", "/api/v1/search?profile_id="+profile.ID, "", cookie, "", nil)
	empty := f.request("GET", "/api/v1/search?profile_id="+profile.ID+"&q=", "", cookie, "", nil)
	requireStatus(t, omitted, 200)
	requireStatus(t, empty, 200)
	if omitted.Body.String() != empty.Body.String() {
		t.Fatal("omitted and empty q returned different index results")
	}
	var all scans.SearchPage
	if err := json.Unmarshal(omitted.Body.Bytes(), &all); err != nil || len(all.Entries) != 6 {
		t.Fatal("empty search lost indexed entries", all, err)
	}
	var folderID string
	for _, entry := range all.Entries {
		if entry.Name == "nested" {
			folderID = entry.ID
		}
	}
	if folderID == "" {
		t.Fatal("indexed fixture folder missing")
	}
	scoped := f.request("GET", "/api/v1/search?profile_id="+profile.ID+"&directory_id="+folderID+"&category=document", "", cookie, "", nil)
	requireStatus(t, scoped, 200)
	var scopedPage scans.SearchPage
	if err := json.Unmarshal(scoped.Body.Bytes(), &scopedPage); err != nil || len(scopedPage.Entries) != 1 || scopedPage.Entries[0].Name != "inside.txt" {
		t.Fatal("folder/category-only query returned wrong entries", scopedPage, err)
	}
	for _, query := range []string{
		"?profile_id=" + profile.ID + "&directory_id=" + folderID + "&direct_only=true&categories=document,video",
		"?profile_id=" + profile.ID + "&directory_id=" + folderID + "&categories=video,document",
	} {
		w := f.request("GET", "/api/v1/search"+query, "", cookie, "", nil)
		requireStatus(t, w, 200)
		var found scans.SearchPage
		if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || len(found.Entries) != 1 || found.Entries[0].Name != "inside.txt" {
			t.Fatal("multi-type search failed", found, err)
		}
	}
	for _, query := range []string{"?direct_only=no", "?categories=", "?categories=video,invalid", "?category=video&categories=image"} {
		requireStatus(t, f.request("GET", "/api/v1/search"+query, "", cookie, "", nil), 400)
	}
	for _, query := range []string{"?q=file&limit=0", "?q=file&sort=size", "?q=file&q=another", "?q=file&url=https://invalid.example/", "?q=file&directory_id=root"} {
		requireStatus(t, f.request("GET", "/api/v1/search"+query, "", cookie, "", nil), 400)
	}
	for _, format := range []string{"json", "csv"} {
		w := f.request("GET", "/api/v1/scans/"+first.ID+"/export?format="+format, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Body.String(), "inside.txt") {
			t.Fatal("export missing contents or attachment header")
		}
	}
	_, browseToken := f.createToken(t, cookie, session, profile.ID, []string{"browse:read"})
	tokenHeaders := map[string]string{"Authorization": "Bearer " + browseToken}
	requireStatus(t, f.request("GET", "/api/v1/scans/"+first.ID, "", nil, "", tokenHeaders), 200)
	requireStatus(t, f.request("POST", "/api/v1/scans/"+first.ID+"/cancel", `{}`, nil, "", tokenHeaders), 403)
	_, contentToken := f.createToken(t, cookie, session, profile.ID, []string{"content:read"})
	requireStatus(t, f.request("GET", "/api/v1/search?q=file", "", nil, "", map[string]string{"Authorization": "Bearer " + contentToken}), 403)
	requireStatus(t, f.request("GET", "/api/v1/search", "", nil, "", map[string]string{"Authorization": "Bearer " + contentToken}), 403)
	other := f.createProfile(t, cookie, session, true)
	_, foreign := f.createToken(t, cookie, session, other.ID, []string{"browse:read", "scan:write"})
	foreignHeaders := map[string]string{"Authorization": "Bearer " + foreign, "Idempotency-Key": "foreign"}
	requireStatus(t, f.request("GET", "/api/v1/scans/"+first.ID, "", nil, "", foreignHeaders), 404)
	requireStatus(t, f.request("GET", "/api/v1/scans/"+first.ID+"/export", "", nil, "", foreignHeaders), 404)
	requireStatus(t, f.request("POST", path, `{}`, nil, "", foreignHeaders), 404)
	w := f.request("GET", "/api/v1/search?q=file", "", nil, "", foreignHeaders)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "file2.txt") {
		t.Fatal("search leaked another profile")
	}
	w = f.request("GET", "/api/v1/search", "", nil, "", foreignHeaders)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "file2.txt") || strings.Contains(w.Body.String(), profile.ID) {
		t.Fatal("empty search leaked another profile")
	}
	requireStatus(t, f.request("GET", "/api/v1/search?profile_id="+profile.ID, "", nil, "", foreignHeaders), 404)
	for _, body := range []string{`{"max_entries":0}`, `{"max_seconds":0}`, `{"max_depth":65}`, `{"directory_id":"https://invalid.example/"}`} {
		w := f.request("POST", path, body, cookie, session.CSRFToken, map[string]string{"Idempotency-Key": "invalid"})
		if w.Code != 400 && w.Code != 404 {
			t.Fatalf("invalid scan accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
