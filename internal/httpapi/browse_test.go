package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/upstream"
)

type browseFixtureFetcher struct {
	handler  http.Handler
	mu       sync.Mutex
	requests []string
}

func (f *browseFixtureFetcher) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	target, err := root.Resolve(path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	if options.Headers != nil {
		request.Header = options.Headers.Clone()
	}
	if options.Credentials != nil {
		request.SetBasicAuth(options.Credentials.Username, options.Credentials.Password)
	}
	f.mu.Lock()
	f.requests = append(f.requests, method+" "+target.EscapedPath())
	f.mu.Unlock()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, request)
	response := w.Result()
	response.Request = request
	return response, nil
}

func (f *browseFixtureFetcher) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func newBrowseFixture(t *testing.T) (*authFixture, *browseFixtureFetcher, *http.Cookie, auth.Session, profiles.Profile) {
	t.Helper()
	f := newAuthFixture(t, "https")
	fetcher := &browseFixtureFetcher{handler: testfixture.Handler(testfixture.Config{Format: testfixture.ApacheTable, Username: "private-fixture-user", Password: "private-fixture-password", Files: map[string]testfixture.File{
		"/root/file2.txt":         {Data: []byte("0123456789"), ContentType: "text/plain"},
		"/root/file10.txt":        {Data: []byte("second"), ContentType: "text/plain"},
		"/root/page.html":         {Data: []byte("<!DOCTYPE html><html><script>alert('fixture')</script></html>"), ContentType: "text/html"},
		"/root/picture.png":       {Data: []byte("\x89PNG\r\n\x1a\nsynthetic picture bytes"), ContentType: "image/png"},
		"/root/nested/inside.txt": {Data: []byte("nested fixture"), ContentType: "text/plain"},
	}})}
	f.server.Upstream = fetcher
	f.server.Catalog = catalog.New(f.server.Store.DB, f.server.Profiles, fetcher)
	f.server.Content = content.New(fetcher, content.Limits{})
	f.handler = f.server.Handler(http.NotFoundHandler())
	cookie, session := f.login(t)
	profile := f.createProfile(t, cookie, session, true)
	return f, fetcher, cookie, session, profile
}

func browsePage(t *testing.T, f *authFixture, profile string, cookie *http.Cookie, query string, headers map[string]string) catalog.Page {
	t.Helper()
	w := f.request(http.MethodGet, "/api/v1/profiles/"+profile+"/browse"+query, "", cookie, "", headers)
	requireStatus(t, w, 200)
	var page catalog.Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func findEntry(t *testing.T, page catalog.Page, name string) listing.Entry {
	t.Helper()
	for _, entry := range page.Entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatal("fixture entry missing", name)
	return listing.Entry{}
}

func contentPath(profile string, entry listing.Entry) string {
	return "/api/v1/profiles/" + profile + "/entries/" + entry.ID + "/content"
}

func TestBrowseSnapshotPaginationFiltersAndCursorBinding(t *testing.T) {
	f, fetcher, cookie, _, profile := newBrowseFixture(t)
	page := browsePage(t, f, profile.ID, cookie, "?limit=2", nil)
	if page.TotalEntries != 5 || len(page.Entries) != 2 || page.Entries[0].Name != "nested" || page.Entries[1].Name != "file2.txt" || page.NextCursor == nil || page.SnapshotID == "" || page.DirectoryID != "root" || page.ParentID != nil || page.Stale {
		t.Fatal("incorrect first page", page)
	}
	second := browsePage(t, f, profile.ID, cookie, "?limit=2&cursor="+url.QueryEscape(*page.NextCursor), nil)
	if second.SnapshotID != page.SnapshotID || len(second.Entries) != 2 || second.Entries[0].Name != "file10.txt" || second.Entries[1].Name != "page.html" {
		t.Fatal("pagination did not retain stable ordering", second)
	}
	if len(fetcher.calls()) != 1 {
		t.Fatal("cursor page fetched a second listing")
	}
	filtered := browsePage(t, f, profile.ID, cookie, "?show_folders=false&extensions=txt&name=FILE&sort=name&direction=desc", nil)
	if filtered.TotalEntries != 2 || len(filtered.Entries) != 2 || filtered.Entries[0].Name != "file10.txt" || filtered.Entries[1].Name != "file2.txt" {
		t.Fatal("combined filters or natural sort failed", filtered)
	}
	nested := browsePage(t, f, profile.ID, cookie, "?directory_id="+page.Entries[0].ID, nil)
	if len(nested.Entries) != 1 || nested.Entries[0].Name != "inside.txt" || nested.ParentID == nil || *nested.ParentID != "root" {
		t.Fatal("directory navigation failed", nested)
	}
	w := f.request("GET", "/api/v1/profiles/"+profile.ID+"/browse?limit=2&name=changed&cursor="+url.QueryEscape(*page.NextCursor), "", cookie, "", nil)
	requireStatus(t, w, 409)
	var problem map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem["code"] != "snapshot_expired" {
		t.Fatal(problem)
	}
	otherCookie, _ := f.login(t)
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+profile.ID+"/browse?limit=2&cursor="+url.QueryEscape(*page.NextCursor), "", otherCookie, "", nil), 409)
}

func TestEntryMetadataAndBrowserContentRoutes(t *testing.T) {
	f, fetcher, cookie, _, profile := newBrowseFixture(t)
	page := browsePage(t, f, profile.ID, cookie, "", nil)
	entry := findEntry(t, page, "file2.txt")
	w := f.request("GET", "/api/v1/profiles/"+profile.ID+"/entries/"+entry.ID, "", cookie, "", nil)
	requireStatus(t, w, 200)
	var meta listing.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Size == nil || *meta.Size != 10 || !meta.SizeExact || meta.Provenance != "response_headers" || meta.ModifiedAt == nil || !meta.TimezoneKnown {
		t.Fatal("metadata response did not include exact HEAD metadata", meta)
	}
	calls := fetcher.calls()
	if calls[len(calls)-1] != "HEAD /root/file2.txt" {
		t.Fatal("metadata did not use HEAD", calls)
	}
	path := contentPath(profile.ID, entry)
	w = f.request("GET", path, "", cookie, "", nil)
	requireStatus(t, w, 200)
	if w.Body.String() != "0123456789" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !strings.Contains(w.Header().Get("Content-Disposition"), "file2.txt") || w.Header().Get("Content-Length") != "10" {
		t.Fatal("incorrect attachment response", w.Header(), w.Body.String())
	}
	w = f.request("HEAD", path, "", cookie, "", nil)
	requireStatus(t, w, 200)
	if w.Body.Len() != 0 || w.Header().Get("Content-Length") != "10" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("HEAD did not preserve headers without a body")
	}
	w = f.request("GET", path, "", cookie, "", map[string]string{"Range": "bytes=2-5"})
	requireStatus(t, w, 206)
	if w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" || w.Header().Get("Content-Length") != "4" {
		t.Fatal("range response mismatch", w.Header(), w.Body.String())
	}
	w = f.request("GET", path, "", cookie, "", map[string]string{"Range": "bytes=99-100"})
	requireStatus(t, w, 416)
	if w.Header().Get("Content-Range") != "bytes */10" || !strings.Contains(w.Body.String(), `"range_not_satisfiable"`) {
		t.Fatal("invalid unsatisfiable range response", w.Header(), w.Body.String())
	}
}

func TestPreviewPoliciesAndContentQueryValidation(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	page := browsePage(t, f, profile.ID, cookie, "", nil)
	html := findEntry(t, page, "page.html")
	picture := findEntry(t, page, "picture.png")
	requireStatus(t, f.request("GET", contentPath(profile.ID, html)+"?disposition=preview", "", cookie, "", nil), 415)
	w := f.request("GET", contentPath(profile.ID, html), "", cookie, "", nil)
	requireStatus(t, w, 200)
	if w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("active content escaped attachment policy", w.Header())
	}
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+profile.ID, `{"previews_enabled":false}`, cookie, session.CSRFToken, nil), 200)
	before := len(fetcher.calls())
	requireStatus(t, f.request("GET", contentPath(profile.ID, picture)+"?disposition=preview", "", cookie, "", nil), 409)
	if len(fetcher.calls()) != before {
		t.Fatal("disabled preview reached upstream")
	}
	for _, query := range []string{"?url=https://attacker.example.test/private", "?disposition=inline", "?disposition=preview&disposition=attachment", "?filename=chosen.txt"} {
		t.Run(query, func(t *testing.T) {
			before := len(fetcher.calls())
			requireStatus(t, f.request("GET", contentPath(profile.ID, picture)+query, "", cookie, "", nil), 400)
			if len(fetcher.calls()) != before {
				t.Fatal("invalid query reached upstream")
			}
		})
	}
	requireStatus(t, f.request("GET", contentPath(profile.ID, findEntry(t, page, "nested")), "", cookie, "", nil), 400)
}

func TestBrowseAndContentTokenPermissionsAndCrossProfileIDs(t *testing.T) {
	f, fetcher, cookie, session, one := newBrowseFixture(t)
	two := f.createProfile(t, cookie, session, true)
	page := browsePage(t, f, one.ID, cookie, "", nil)
	entry := findEntry(t, page, "file2.txt")
	_, browseRaw := f.createToken(t, cookie, session, one.ID, []string{"browse:read"})
	browseHeaders := map[string]string{"Authorization": "Bearer " + browseRaw}
	_, contentRaw := f.createToken(t, cookie, session, one.ID, []string{"content:read"})
	contentHeaders := map[string]string{"Authorization": "Bearer " + contentRaw}
	browsePage(t, f, one.ID, nil, "", browseHeaders)
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+one.ID+"/entries/"+entry.ID, "", nil, "", browseHeaders), 200)
	requireStatus(t, f.request("GET", contentPath(one.ID, entry), "", nil, "", browseHeaders), 403)
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+one.ID+"/browse", "", nil, "", contentHeaders), 403)
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+one.ID+"/entries/"+entry.ID, "", nil, "", contentHeaders), 403)
	requireStatus(t, f.request("GET", contentPath(one.ID, entry), "", nil, "", contentHeaders), 200)
	for _, tc := range []struct {
		path    string
		headers map[string]string
		cookie  *http.Cookie
	}{{"/api/v1/profiles/" + two.ID + "/browse", browseHeaders, nil}, {contentPath(two.ID, entry), contentHeaders, nil}, {"/api/v1/profiles/" + two.ID + "/entries/" + entry.ID, nil, cookie}, {contentPath(two.ID, entry), nil, cookie}} {
		before := len(fetcher.calls())
		requireStatus(t, f.request("GET", tc.path, "", tc.cookie, "", tc.headers), 404)
		if len(fetcher.calls()) != before {
			t.Fatal("foreign identity reached upstream")
		}
	}
}

func TestLockedProfilesInvalidQueriesAndSourceGeneration(t *testing.T) {
	f, fetcher, cookie, session, profile := newBrowseFixture(t)
	page := browsePage(t, f, profile.ID, cookie, "", nil)
	entry := findEntry(t, page, "file2.txt")
	for _, query := range []string{"?url=https://attacker.example.test/", "?limit=0", "?limit=1001", "?limit=2&limit=3", "?show_folders=1", "?sort=unsupported", "?modified_after=not-a-date", "?min_size=-1", "?min_size=10&max_size=1"} {
		t.Run(query, func(t *testing.T) {
			before := len(fetcher.calls())
			requireStatus(t, f.request("GET", "/api/v1/profiles/"+profile.ID+"/browse"+query, "", cookie, "", nil), 400)
			if len(fetcher.calls()) != before {
				t.Fatal("invalid browse query reached upstream")
			}
		})
	}
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+profile.ID+"/browse?directory_id="+url.QueryEscape("https://attacker.example.test/"), "", cookie, "", nil), 404)
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+profile.ID, `{"root_url":"https://files.example.test/new-root/","credentials":{"username":"private-fixture-user","password":"private-fixture-password","remember":true}}`, cookie, session.CSRFToken, nil), 200)
	before := len(fetcher.calls())
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+profile.ID+"/entries/"+entry.ID, "", cookie, "", nil), 404)
	requireStatus(t, f.request("GET", contentPath(profile.ID, entry), "", cookie, "", nil), 404)
	if len(fetcher.calls()) != before {
		t.Fatal("old generation entry reached new source")
	}
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+profile.ID, `{"credentials":null}`, cookie, session.CSRFToken, nil), 200)
	w := f.request("GET", "/api/v1/profiles/"+profile.ID+"/browse", "", cookie, "", nil)
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), `"credentials_required"`) || len(fetcher.calls()) != before {
		t.Fatal("locked profile accessed upstream", w.Body.String())
	}
	requireStatus(t, f.request("GET", contentPath(profile.ID, entry), "", cookie, "", nil), 409)
}
