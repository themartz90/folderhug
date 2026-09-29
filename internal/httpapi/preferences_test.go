package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/preferences"
	"github.com/themartz90/folderhug/internal/profiles"
)

func preferencesHTTPFixture(t *testing.T) (*authFixture, *http.Cookie, auth.Session, profiles.Profile) {
	t.Helper()
	f, _, cookie, session, profile := newBrowseFixture(t)
	f.server.Preferences = preferences.New(f.server.Store.DB, f.server.Profiles, f.server.Catalog)
	f.server.StatusProvider = func(context.Context) (preferences.Status, error) {
		return preferences.Status{ImageThumbnails: true, VideoWorker: "disabled", CacheMaxBytes: 1 << 30, StreamMaxBytes: 1 << 40, ServerDownloads: true, SegmentedDownloads: true}, nil
	}
	f.handler = f.server.Handler(http.NotFoundHandler())
	return f, cookie, session, profile
}

func TestPreferencesHTTPContractAndRequiredFields(t *testing.T) {
	f, cookie, session, profile := preferencesHTTPFixture(t)
	w := f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"theme":"system"`) || !strings.Contains(w.Body.String(), `"profile_views":{}`) {
		t.Fatal(w.Body.String())
	}
	body := `{"theme":"dark","profile_views":{"` + profile.ID + `":{"view":"grid","sort":"name","direction":"asc","folders_first":false}}}`
	w = f.request("PUT", "/api/v1/preferences", body, cookie, session.CSRFToken, nil)
	requireStatus(t, w, 200)
	var value preferences.Preferences
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil || value.Theme != "dark" || value.ProfileViews[profile.ID].FoldersFirst {
		t.Fatal(value, err)
	}
	for _, invalid := range []string{`{}`, `{"theme":"system"}`, `{"theme":"system","profile_views":null}`, `{"theme":"system","profile_views":{},"unknown":true}`, `{"theme":"system","profile_views":{"` + profile.ID + `":{"view":"grid","sort":"name","direction":"asc"}}}`, `{"theme":"system","profile_views":{"` + profile.ID + `":{"view":"grid","sort":"name","direction":"asc","folders_first":false,"unknown":true}}}`} {
		requireStatus(t, f.request("PUT", "/api/v1/preferences", invalid, cookie, session.CSRFToken, nil), 400)
	}
	requireStatus(t, f.request("PUT", "/api/v1/preferences", body, cookie, "", nil), 403)
	w = f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"theme":"dark"`) {
		t.Fatal("failed update changed preferences")
	}
}

func TestPreferencesAppearanceAndLegacyWrites(t *testing.T) {
	f, cookie, session, _ := preferencesHTTPFixture(t)
	w := f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	for _, expected := range []string{`"accent":"amber"`, `"density":"comfortable"`, `"filter_presets":[]`, `"profile_order":[]`} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("default preferences missing %s: %s", expected, w.Body.String())
		}
	}
	body := `{"theme":"dark","profile_views":{},"accent":"rose","density":"compact","filter_presets":[{"id":"videos","name":"Videos","filters":{"category":"video","show_folders":false}}],"profile_order":[]}`
	requireStatus(t, f.request("PUT", "/api/v1/preferences", body, cookie, session.CSRFToken, nil), 200)
	w = f.request("PUT", "/api/v1/preferences", `{"theme":"light","profile_views":{}}`, cookie, session.CSRFToken, nil)
	requireStatus(t, w, 200)
	for _, expected := range []string{`"theme":"light"`, `"accent":"rose"`, `"density":"compact"`, `"id":"videos"`} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("legacy save lost %s: %s", expected, w.Body.String())
		}
	}
}

func TestPreferencesStrictExtendedInput(t *testing.T) {
	f, cookie, session, profile := preferencesHTTPFixture(t)
	for _, fields := range []string{
		`"accent":null`, `"accent":""`, `"accent":"red"`, `"density":null`, `"density":""`, `"density":"dense"`,
		`"filter_presets":null`, `"profile_order":null`, `"profile_order":[null]`,
		`"profile_order":["` + profile.ID + `","` + profile.ID + `"]`,
		`"accent":"blue","accent":"rose"`,
		`"filter_presets":[null]`, `"filter_presets":[{}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":null}]`,
		`"filter_presets":[{"id":"v","name":"Videos","profile_id":null,"filters":{}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","profile_id":"","filters":{}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","extra":true,"filters":{}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"extra":true}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"category":"movies"}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"category":"video","category":"audio"}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"show_folders":null}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"name":null}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"min_size":null}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"min_size":-1}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"min_size":1.5}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"min_size":9223372036854775808}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"modified_after":"yesterday"}}]`,
		`"filter_presets":[{"id":"v","name":"Videos","filters":{"extensions":".mp4"}}]`,
	} {
		t.Run(fields, func(t *testing.T) {
			body := `{"theme":"system","profile_views":{},` + fields + `}`
			requireStatus(t, f.request("PUT", "/api/v1/preferences", body, cookie, session.CSRFToken, nil), 400)
		})
	}
	tooLarge := `{"theme":"system","profile_views":{},"accent":"` + strings.Repeat("a", 64<<10) + `"}`
	requireStatus(t, f.request("PUT", "/api/v1/preferences", tooLarge, cookie, session.CSRFToken, nil), 400)
	requireStatus(t, f.request("PUT", "/api/v1/preferences", `{"theme":"system","profile_views":{},"profile_order":["missing"]}`, cookie, session.CSRFToken, nil), 404)
	w := f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"accent":"amber"`) || !strings.Contains(w.Body.String(), `"filter_presets":[]`) {
		t.Fatal("invalid updates changed preferences", w.Body.String())
	}
}

func TestPreferencesHTTPStoredLegacyDefaultsAndProfileRemoval(t *testing.T) {
	f, cookie, session, profile := preferencesHTTPFixture(t)
	if _, err := f.server.Store.DB.Exec(`INSERT INTO owner_preferences(id,settings) VALUES(1,?)`, `{"theme":"dark","profile_views":{}}`); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"accent":"amber"`) || !strings.Contains(w.Body.String(), `"theme":"dark"`) {
		t.Fatal("legacy defaults missing", w.Body.String())
	}
	body := `{"theme":"dark","profile_views":{},"accent":"amber","density":"compact","profile_order":["` + profile.ID + `"],"filter_presets":[{"id":"videos","name":"Videos","profile_id":"` + profile.ID + `","filters":{"category":"video","extensions":"mp4,mkv","min_size":0,"max_size":4096,"modified_after":"2026-01-01T00:00:00Z","modified_before":"2026-12-31T00:00:00Z","show_folders":false}}]}`
	w = f.request("PUT", "/api/v1/preferences", body, cookie, session.CSRFToken, nil)
	requireStatus(t, w, 200)
	var written preferences.Preferences
	if err := json.Unmarshal(w.Body.Bytes(), &written); err != nil || len(written.FilterPresets) != 1 || written.FilterPresets[0].Filters.ShowFolders == nil || *written.FilterPresets[0].Filters.ShowFolders {
		t.Fatal("filters did not round trip", written, err)
	}
	w = f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"id":"videos"`) || !strings.Contains(w.Body.String(), `"accent":"amber"`) {
		t.Fatal("stored preferences missing", w.Body.String())
	}
	requireStatus(t, f.request("DELETE", "/api/v1/profiles/"+profile.ID, "", cookie, session.CSRFToken, nil), 204)
	w = f.request("GET", "/api/v1/preferences", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), profile.ID) || !strings.Contains(w.Body.String(), `"filter_presets":[]`) || !strings.Contains(w.Body.String(), `"profile_order":[]`) {
		t.Fatal("deleted profile references retained", w.Body.String())
	}
}

func TestBookmarksHTTPValidationAndStaleGeneration(t *testing.T) {
	f, cookie, session, profile := preferencesHTTPFixture(t)
	page := browsePage(t, f, profile.ID, cookie, "", nil)
	folder := findEntry(t, page, "nested")
	file := findEntry(t, page, "file2.txt")
	create := func(directory, name string) *preferences.Bookmark {
		body, _ := json.Marshal(preferences.BookmarkCreate{ProfileID: profile.ID, DirectoryID: directory, Name: name})
		w := f.request("POST", "/api/v1/bookmarks", string(body), cookie, session.CSRFToken, nil)
		requireStatus(t, w, 201)
		var bookmark preferences.Bookmark
		if err := json.Unmarshal(w.Body.Bytes(), &bookmark); err != nil {
			t.Fatal(err)
		}
		return &bookmark
	}
	bookmark := create(folder.ID, "Nested folder")
	w := f.request("GET", "/api/v1/bookmarks", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), bookmark.ID) {
		t.Fatal("created bookmark missing")
	}
	body, _ := json.Marshal(preferences.BookmarkCreate{ProfileID: profile.ID, DirectoryID: file.ID, Name: "Wrong kind"})
	requireStatus(t, f.request("POST", "/api/v1/bookmarks", string(body), cookie, session.CSRFToken, nil), 400)
	requireStatus(t, f.request("POST", "/api/v1/bookmarks", `{}`, cookie, session.CSRFToken, nil), 400)
	requireStatus(t, f.request("POST", "/api/v1/bookmarks", `{"profile_id":"`+profile.ID+`","directory_id":"root","name":"Root","position":null}`, cookie, session.CSRFToken, nil), 400)
	requireStatus(t, f.request("DELETE", "/api/v1/bookmarks/"+bookmark.ID, "", cookie, "", nil), 403)
	requireStatus(t, f.request("DELETE", "/api/v1/bookmarks/"+bookmark.ID, "", cookie, session.CSRFToken, nil), 204)
	requireStatus(t, f.request("DELETE", "/api/v1/bookmarks/"+bookmark.ID, "", cookie, session.CSRFToken, nil), 404)
	bookmark = create("root", "Root")
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+profile.ID, `{"root_url":"https://changed.example.test/new/"}`, cookie, session.CSRFToken, nil), 200)
	w = f.request("GET", "/api/v1/bookmarks", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), bookmark.ID) || !strings.Contains(w.Body.String(), `"bookmarks":[]`) {
		t.Fatal("changed source kept stale bookmark", w.Body.String())
	}
}

func TestPreferencesBookmarksAndStatusAreOwnerOnly(t *testing.T) {
	f, cookie, session, profile := preferencesHTTPFixture(t)
	_, raw := f.createToken(t, cookie, session, profile.ID, []string{"browse:read", "content:read", "scan:write", "downloads:write", "credentials:unlock"})
	for _, tc := range []struct{ method, path, body string }{{"GET", "/api/v1/preferences", ""}, {"PUT", "/api/v1/preferences", `{"theme":"system","profile_views":{}}`}, {"GET", "/api/v1/bookmarks", ""}, {"POST", "/api/v1/bookmarks", `{}`}, {"DELETE", "/api/v1/bookmarks/unknown", ""}, {"GET", "/api/v1/status", ""}} {
		requireStatus(t, f.request(tc.method, tc.path, tc.body, nil, "", map[string]string{"Authorization": "Bearer " + raw}), 403)
		requireStatus(t, f.request(tc.method, tc.path, tc.body, nil, "", nil), 401)
	}
}

func TestStatusCapabilitiesAndFailureDoNotRevealPrivateConfiguration(t *testing.T) {
	f, cookie, _, _ := preferencesHTTPFixture(t)
	for _, worker := range []string{"disabled", "ready", "unavailable"} {
		f.server.StatusProvider = func(context.Context) (preferences.Status, error) {
			return preferences.Status{ImageThumbnails: true, VideoWorker: worker, CacheMaxBytes: 1073741824, StreamMaxBytes: 1099511627776, ServerDownloads: true, SegmentedDownloads: true}, nil
		}
		w := f.request("GET", "/api/v1/status", "", cookie, "", nil)
		requireStatus(t, w, 200)
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 6 || payload["video_worker"] != worker || payload["image_thumbnails"] != true {
			t.Fatal(payload)
		}
		for _, private := range []string{"root_url", "password", "username", "data_dir", "downloads_dir", "cache_dir", "files.example.test", "private-fixture"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("status exposed private configuration")
			}
		}
	}
	f.server.StatusProvider = func(context.Context) (preferences.Status, error) {
		return preferences.Status{}, errors.New("private error C:/private/path and private-password")
	}
	w := f.request("GET", "/api/v1/status", "", cookie, "", nil)
	requireStatus(t, w, 503)
	if strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "C:/") {
		t.Fatal("provider failure leaked details")
	}
	f.server.StatusProvider = nil
	requireStatus(t, f.request("GET", "/api/v1/status", "", cookie, "", nil), 503)
}
