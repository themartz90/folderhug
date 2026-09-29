package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/upstream"
)

type thumbnailRouteFetcher struct {
	base *browseFixtureFetcher
	gate <-chan struct{}
}

func (f thumbnailRouteFetcher) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	if f.gate != nil && path != "" {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.base.Do(ctx, root, method, path, options)
}

func thumbnailHTTPFixture(t *testing.T, gate <-chan struct{}) (*authFixture, *http.Cookie, auth.Session, profiles.Profile, map[string]listing.Entry) {
	t.Helper()
	f, base, cookie, session, profile := newBrowseFixture(t)
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 180, G: 40, B: 20, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	base.handler = testfixture.Handler(testfixture.Config{Username: "private-fixture-user", Password: "private-fixture-password", Files: map[string]testfixture.File{"/root/photo.jpg": {Data: encoded.Bytes(), ContentType: "image/jpeg"}, "/root/bad.png": {Data: []byte("not an image"), ContentType: "image/png"}}})
	service, err := thumbnails.New(f.server.Store.DB, f.server.Profiles, f.server.Auth, f.server.Catalog, thumbnailRouteFetcher{base: base, gate: gate}, nil, thumbnails.Config{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	f.server.Thumbnails = service
	f.handler = f.server.Handler(http.NotFoundHandler())
	page := browsePage(t, f, profile.ID, cookie, "", nil)
	entries := map[string]listing.Entry{}
	for _, entry := range page.Entries {
		entries[entry.Name] = entry
	}
	return f, cookie, session, profile, entries
}

func requestThumbnailHTTP(t *testing.T, f *authFixture, cookie *http.Cookie, session auth.Session, profileID, entryID string) thumbnails.Thumbnail {
	t.Helper()
	w := f.request("POST", "/api/v1/profiles/"+profileID+"/thumbnails", `{"entry_ids":["`+entryID+`"]}`, cookie, session.CSRFToken, nil)
	requireStatus(t, w, 202)
	var response struct {
		Thumbnails []thumbnails.Thumbnail `json:"thumbnails"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Thumbnails) != 1 {
		t.Fatal(response, err)
	}
	return response.Thumbnails[0]
}

func thumbnailNetworkRequest(t *testing.T, f *authFixture, server *httptest.Server, method, path string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = f.server.Config.PublicURL.Host
	request.AddCookie(cookie)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func TestThumbnailHTTPPendingReadyJPEGAndHEAD(t *testing.T) {
	gate := make(chan struct{})
	f, cookie, session, profile, entries := thumbnailHTTPFixture(t, gate)
	item := requestThumbnailHTTP(t, f, cookie, session, profile.ID, entries["photo.jpg"].ID)
	if item.State != "pending" {
		t.Fatal(item)
	}
	path := "/api/v1/thumbnails/" + item.ID
	w := f.request("GET", path, "", cookie, "", nil)
	requireStatus(t, w, 202)
	if w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"state":"pending"`) {
		t.Fatal("pending response lacks retry guidance")
	}
	close(gate)
	server := httptest.NewServer(f.handler)
	defer server.Close()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var ready *http.Response
	var jpegBytes []byte
	for {
		response, body := thumbnailNetworkRequest(t, f, server, "GET", path, cookie)
		if response.StatusCode == 200 {
			ready = response
			jpegBytes = body
			break
		}
		if response.StatusCode != 202 {
			t.Fatal("generation failed", response.StatusCode, string(body))
		}
		select {
		case <-deadline.C:
			t.Fatal("queued image did not finish")
		case <-tick.C:
		}
	}
	if ready.Header.Get("Content-Type") != "image/jpeg" || ready.Header.Get("Cache-Control") != "no-store" || ready.ContentLength != int64(len(jpegBytes)) {
		t.Fatal("invalid JPEG response headers", ready.Header)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil || decoded.Bounds().Dx() != 40 || decoded.Bounds().Dy() != 30 {
		t.Fatal("invalid generated JPEG", err)
	}
	head, body := thumbnailNetworkRequest(t, f, server, "HEAD", path, cookie)
	if head.StatusCode != 200 || head.Header.Get("Content-Type") != "image/jpeg" || head.ContentLength != ready.ContentLength || len(body) != 0 {
		t.Fatal("HEAD did not preserve JPEG metadata without body", head.StatusCode, head.Header, len(body))
	}
	cache := requestThumbnailHTTP(t, f, cookie, session, profile.ID, entries["photo.jpg"].ID)
	if cache.ID != item.ID || cache.State != "ready" {
		t.Fatal("HTTP request did not use cached thumbnail", cache)
	}
}

func TestThumbnailHTTPUnavailableAndDisabled(t *testing.T) {
	f, cookie, session, profile, entries := thumbnailHTTPFixture(t, nil)
	item := requestThumbnailHTTP(t, f, cookie, session, profile.ID, entries["bad.png"].ID)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		w := f.request("GET", "/api/v1/thumbnails/"+item.ID, "", cookie, "", nil)
		if w.Code == 404 {
			break
		}
		requireStatus(t, w, 202)
		select {
		case <-deadline.C:
			t.Fatal("malformed image did not finish")
		case <-tick.C:
		}
	}
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+profile.ID, `{"previews_enabled":false}`, cookie, session.CSRFToken, nil), 200)
	disabled := requestThumbnailHTTP(t, f, cookie, session, profile.ID, entries["photo.jpg"].ID)
	if disabled.State != "disabled" {
		t.Fatal(disabled)
	}
	requireStatus(t, f.request("GET", "/api/v1/thumbnails/"+disabled.ID, "", cookie, "", nil), 404)
}

func TestThumbnailHTTPProfileScopeAndStrictInputs(t *testing.T) {
	f, cookie, session, profile, entries := thumbnailHTTPFixture(t, nil)
	item := requestThumbnailHTTP(t, f, cookie, session, profile.ID, entries["photo.jpg"].ID)
	other := f.createProfile(t, cookie, session, true)
	_, foreign := f.createToken(t, cookie, session, other.ID, []string{"content:read"})
	_, browseOnly := f.createToken(t, cookie, session, profile.ID, []string{"browse:read"})
	for _, tc := range []struct {
		secret string
		status int
	}{{foreign, 404}, {browseOnly, 403}} {
		headers := map[string]string{"Authorization": "Bearer " + tc.secret}
		requireStatus(t, f.request("GET", "/api/v1/thumbnails/"+item.ID, "", nil, "", headers), tc.status)
		requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/thumbnails", `{"entry_ids":["`+entries["photo.jpg"].ID+`"]}`, nil, "", headers), tc.status)
	}
	for _, body := range []string{`{}`, `{"entry_ids":[]}`, `{"entry_ids":null}`, `{"entry_ids":[null]}`, `{"entry_ids":["` + entries["photo.jpg"].ID + `","` + entries["photo.jpg"].ID + `"]}`, `{"entry_ids":["` + entries["photo.jpg"].ID + `"],"url":"https://attacker.example.test/"}`} {
		t.Run(body, func(t *testing.T) {
			requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/thumbnails", body, cookie, session.CSRFToken, nil), 400)
		})
	}
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/thumbnails", `{"entry_ids":["`+entries["photo.jpg"].ID+`"]}`, cookie, "", nil), 403)
	requireStatus(t, f.request("GET", "/api/v1/thumbnails/unknown", "", cookie, "", nil), 404)
}
