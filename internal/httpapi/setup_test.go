package httpapi

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/profiles"
)

func unclaimedFixture(t *testing.T) *authFixture {
	t.Helper()
	f := newAuthFixture(t, "https")
	// HTTP tests isolate the claim state; startup tests verify the key on disk.
	_, err := f.server.Store.DB.Exec(`DELETE FROM owner_auth;
		DELETE FROM settings WHERE key='owner_setup_complete';
		INSERT INTO settings(key,value) VALUES('owner_setup_pending','1'),('credential_key_check','synthetic-key-check')`)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBrowserSetupClaimsOnceWithSixCharacters(t *testing.T) {
	f := unclaimedFixture(t)
	w := f.request("GET", "/api/v1/auth/setup", "", nil, "", nil)
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"setup_required":true`) {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, f.request("POST", "/api/v1/auth/setup", `{"password":"abcdef"}`, nil, "", map[string]string{"Origin": "https://evil.example"}), 403)
	requireStatus(t, f.request("POST", "/api/v1/auth/setup", `{"password":"short"}`, nil, "", nil), 400)
	w = f.request("POST", "/api/v1/auth/setup", `{"password":"abcdef","remember_me":true}`, nil, "", nil)
	requireStatus(t, w, 200)
	var session auth.Session
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.ExpiresAt.Before(time.Now().Add(89 * 24 * time.Hour)) {
		t.Fatal("remembered setup login is too short")
	}
	if w.Result().Cookies()[0].Expires.Before(time.Now().Add(364 * 24 * time.Hour)) {
		t.Fatal("remembered cookie must survive renewed inactivity deadlines")
	}
	requireStatus(t, f.request("GET", "/api/v1/auth/session", "", w.Result().Cookies()[0], "", nil), 200)
	requireStatus(t, f.request("POST", "/api/v1/auth/setup", `{"password":"hijack"}`, nil, "", nil), 409)
	if _, err := f.server.Store.DB.Exec("DELETE FROM owner_auth"); err != nil {
		t.Fatal(err)
	}
	w = f.request("GET", "/api/v1/auth/setup", "", nil, "", nil)
	if !strings.Contains(w.Body.String(), `"setup_required":false`) {
		t.Fatal("setup reopened after owner loss")
	}
}

func TestConcurrentBrowserClaimsHaveOneWinner(t *testing.T) {
	f := unclaimedFixture(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- f.request("POST", "/api/v1/auth/setup", `{"password":"abcdef"}`, nil, "", nil).Code })
	}
	wg.Wait()
	close(codes)
	winners := 0
	for code := range codes {
		if code == 200 {
			winners++
		} else if code != 409 {
			t.Fatalf("unexpected claim response %d", code)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winning claims", winners)
	}
}

func TestLogoutAllRevokesBothBrowsers(t *testing.T) {
	f := newAuthFixture(t, "https")
	first, session := f.login(t)
	second, _ := f.login(t)
	principal, err := f.server.Auth.Authenticate(t.Context(), first.Value, "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := f.server.Profiles.Create(t.Context(), profiles.Input{Name: "Synthetic", RootURL: "https://example.test/", AuthType: "basic", Credentials: &profiles.Credentials{Username: "synthetic", Password: "synthetic"}}, principal)
	if err != nil {
		t.Fatal(err)
	}
	_, _, active, cancel, err := f.server.Profiles.Access(t.Context(), profile.ID, profiles.LeaseID(principal))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_, token, err := f.server.Auth.CreateToken(t.Context(), auth.TokenCreate{Name: "Synthetic", ProfileIDs: []string{profile.ID}, Scopes: []string{"browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/api/v1/auth/logout-all", "", nil, "", map[string]string{"Authorization": "Bearer " + token}), 403)
	requireStatus(t, f.request("POST", "/api/v1/auth/logout-all", "", first, "wrong", nil), 403)
	requireStatus(t, f.request("POST", "/api/v1/auth/logout-all", "", first, session.CSRFToken, nil), 204)
	requireAccessCancelled(t, active)
	if _, err := f.server.Auth.Authenticate(t.Context(), "", token); err != nil {
		t.Fatal("logout-all revoked API token", err)
	}
	for _, cookie := range []string{first.Value, second.Value} {
		if _, err := f.server.Auth.Authenticate(t.Context(), cookie, ""); err == nil {
			t.Fatal("session survived logout-all")
		}
	}
}

func TestRememberedHTTPRefreshRotatesCookieAndKeepsCSRF(t *testing.T) {
	f := newAuthFixture(t, "https")
	w := f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`","remember_me":true}`, nil, "", nil)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	var session auth.Session
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request("POST", "/api/v1/auth/refresh", "", cookie, "wrong", nil), 403)
	requireStatus(t, f.request("POST", "/api/v1/auth/refresh", "", cookie, session.CSRFToken, map[string]string{"Origin": "https://evil.example"}), 403)
	if _, err := f.server.Store.DB.Exec("UPDATE auth_sessions SET last_rotated_at=?,expires_at=?", time.Now().Add(-25*time.Hour).Unix(), time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	w = f.request("POST", "/api/v1/auth/refresh", "", cookie, session.CSRFToken, nil)
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == cookie.Value || !cookies[0].Expires.Equal(cookie.Expires) || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("refresh did not rotate with the original absolute expiry")
	}
	var renewed auth.Session
	if err := json.Unmarshal(w.Body.Bytes(), &renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.CSRFToken != session.CSRFToken || renewed.ExpiresAt.Before(time.Now().Add(89*24*time.Hour)) {
		t.Fatal("session identity or inactivity renewal changed")
	}
	w = f.request("POST", "/api/v1/auth/refresh", "", cookie, session.CSRFToken, nil)
	requireStatus(t, w, 200)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("parallel old-cookie response could overwrite rotated cookie")
	}
	requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookies[0], "", nil), 200)
}
