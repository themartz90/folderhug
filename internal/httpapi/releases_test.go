package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/updates"
	"github.com/themartz90/folderhug/internal/upstream"
)

type releaseFailureClient struct{}

func (releaseFailureClient) Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
	return nil, errors.New("offline")
}

type releaseBlockedClient struct{ started chan struct{} }

func (c releaseBlockedClient) Do(ctx context.Context, _ *upstream.Root, _, _ string, _ upstream.Options) (*http.Response, error) {
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLocalReleaseReturnsDuringRemoteLookup(t *testing.T) {
	f := newAuthFixture(t, "http")
	cookie, _ := f.login(t)
	client := releaseBlockedClient{started: make(chan struct{})}
	f.server.BuildInfo = buildinfo.Info{Version: "v1.0.0", Channel: "stable"}
	f.server.Updates = updates.New(client, f.server.BuildInfo, true)
	done := make(chan struct{})
	go func() { f.request("GET", "/api/v1/updates", "", cookie, "", nil); close(done) }()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("lookup did not start")
	}
	start := time.Now()
	w := f.request("GET", "/api/v1/release", "", cookie, "", nil)
	requireStatus(t, w, 200)
	if time.Since(start) > time.Second {
		t.Fatal("local data waited for remote lookup")
	}
	f.server.Updates.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lookup did not stop")
	}
}

func TestReleaseRoutesOwnerOnlyWithoutPreferences(t *testing.T) {
	f := newAuthFixture(t, "http")
	cookie, session := f.login(t)
	profile := f.createProfile(t, cookie, session, false)
	_, raw, err := f.server.Auth.CreateToken(context.Background(), auth.TokenCreate{Name: "Release access", ProfileIDs: []string{profile.ID}, Scopes: []string{"browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/release", "/api/v1/updates"} {
		requireStatus(t, f.request("GET", path, "", nil, "", nil), 401)
		requireStatus(t, f.request("GET", path, "", nil, "", map[string]string{"Authorization": "Bearer " + raw}), 403)
		requireStatus(t, f.request("GET", path, "", cookie, "", nil), 200)
	}
}

func TestReleaseDataIndependentOfRemoteFailure(t *testing.T) {
	f := newAuthFixture(t, "http")
	cookie, _ := f.login(t)
	f.server.BuildInfo = buildinfo.Info{Version: "v1.2.3", Revision: "fixture", Channel: "stable"}
	f.server.Updates = updates.New(releaseFailureClient{}, f.server.BuildInfo, true)
	defer f.server.Updates.Close()
	w := f.request("GET", "/api/v1/updates", "", cookie, "", nil)
	requireStatus(t, w, 200)
	var status updates.Status
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "unknown" {
		t.Fatal(w.Body.String())
	}
	start := time.Now()
	w = f.request("GET", "/api/v1/release", "", cookie, "", nil)
	requireStatus(t, w, 200)
	var data struct {
		Current    buildinfo.Info `json:"current"`
		Highlights any            `json:"highlights"`
	}
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Current.Version != "v1.2.3" || data.Highlights != nil {
		t.Fatal(w.Body.String())
	}
	if time.Since(start) > time.Second {
		t.Fatal("local release data blocked on network")
	}
}

func TestReleaseDefaultsToRunningBuild(t *testing.T) {
	f := newAuthFixture(t, "http")
	cookie, _ := f.login(t)
	w := f.request("GET", "/api/v1/release", "", cookie, "", nil)
	requireStatus(t, w, 200)
	var data struct {
		Current buildinfo.Info `json:"current"`
	}
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Current != buildinfo.Current() {
		t.Fatal(w.Body.String())
	}
}
