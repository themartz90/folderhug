package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/upstream"
)

const ownerPassword = "synthetic owner password"

type authFixture struct {
	server  *Server
	handler http.Handler
	origin  string
}

func newAuthFixture(t *testing.T, scheme string) *authFixture {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	authentication := auth.New(db.DB)
	if err := authentication.Setup(context.Background(), ownerPassword); err != nil {
		t.Fatal(err)
	}
	origin := scheme + "://wrapper.example.test"
	publicURL, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Config: config.Config{PublicURL: publicURL}, Store: db, Auth: authentication, Profiles: profiles.New(db.DB, vault)}
	t.Cleanup(func() { s.Profiles.Close(); db.Close() })
	return &authFixture{server: s, handler: s.Handler(http.NotFoundHandler()), origin: origin}
}

func (f *authFixture) request(method, path, body string, cookie *http.Cookie, csrf string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, f.origin+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:54321"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", f.origin)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	for key, value := range headers {
		if key == "Host" {
			r.Host = value
		} else {
			r.Header.Set(key, value)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f *authFixture) login(t *testing.T) (*http.Cookie, auth.Session) {
	t.Helper()
	w := f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", nil)
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected session cookie, got %d", len(cookies))
	}
	var session auth.Session
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return cookies[0], session
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
}

func requireAccessCancelled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active upstream access was not cancelled")
	}
}

func TestLoginCookieFlagsAndSession(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			f := newAuthFixture(t, scheme)
			cookie, session := f.login(t)
			if !cookie.HttpOnly || cookie.Secure != (scheme == "https") || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.Value == "" {
				t.Fatal("unsafe session cookie flags")
			}
			if !session.Authenticated || session.CSRFToken == "" || !session.ExpiresAt.After(time.Now()) {
				t.Fatal("invalid session response")
			}
			w := f.request("GET", "/api/v1/auth/session", "", cookie, "", nil)
			requireStatus(t, w, 200)
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
				t.Fatal("sensitive response missing cache/request headers")
			}
			if strings.Contains(w.Body.String(), cookie.Value) || strings.Contains(w.Body.String(), ownerPassword) {
				t.Fatal("session response leaked authentication secret")
			}
			requireStatus(t, f.request("GET", "/api/v1/auth/session", "", nil, "", nil), 401)
		})
	}
}

func TestHostOriginAndCSRFGuards(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	for _, headers := range []map[string]string{{"Origin": "https://attacker.example.test"}, {"Origin": ""}} {
		requireStatus(t, f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", headers), 403)
	}
	requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", map[string]string{"Host": "attacker.example.test", "X-Forwarded-Host": "wrapper.example.test", "Forwarded": "host=wrapper.example.test;proto=https"}), 421)
	for _, tc := range []struct {
		csrf    string
		headers map[string]string
	}{{"", nil}, {"wrong-csrf", nil}, {session.CSRFToken, map[string]string{"Origin": "https://attacker.example.test"}}, {session.CSRFToken, map[string]string{"Origin": ""}}} {
		requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, tc.csrf, tc.headers), 403)
		requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", nil), 200)
	}
	w := f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, nil)
	requireStatus(t, w, 204)
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatal("logout did not expire protected cookie")
	}
	requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", nil), 401)
}

func TestMalformedBearerNeverFallsBackToCookie(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, _ := f.login(t)
	for _, value := range []string{"", "Bearer", "Bearer ", "Bearer   ", "Basic abc", "Bearer invalid", "bearer invalid"} {
		t.Run(value, func(t *testing.T) {
			requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", map[string]string{"Authorization": value}), 401)
		})
	}
	r := httptest.NewRequest("GET", f.origin+"/api/v1/auth/session", nil)
	r.AddCookie(cookie)
	r.Header["Authorization"] = []string{"Bearer first", "Bearer second"}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	requireStatus(t, w, 401)
}

func TestLoginThrottleIgnoresUntrustedForwardedHeaders(t *testing.T) {
	f := newAuthFixture(t, "http")
	for i := 0; i < 5; i++ {
		headers := map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('1'+i)), "Forwarded": "for=203.0.113.99;proto=https"}
		requireStatus(t, f.request("POST", "/api/v1/auth/login", `{"password":"incorrect fixture password"}`, nil, "", headers), 401)
	}
	w := f.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", map[string]string{"X-Forwarded-For": "203.0.113.100"})
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("throttle response lacks retry guidance")
	}
	fresh := newAuthFixture(t, "http")
	w = fresh.request("POST", "/api/v1/auth/login", `{"password":"`+ownerPassword+`"}`, nil, "", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "attacker.example.test", "Forwarded": "host=attacker.example.test;proto=https"})
	requireStatus(t, w, 200)
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure {
		t.Fatal("untrusted forwarded proto changed cookie policy")
	}
}

func (f *authFixture) createProfile(t *testing.T, cookie *http.Cookie, session auth.Session, remember bool) profiles.Profile {
	t.Helper()
	body := `{"name":"Synthetic profile","root_url":"https://files.example.test/root/","auth_type":"basic","credentials":{"username":"private-fixture-user","password":"private-fixture-password","remember":false}}`
	if remember {
		body = strings.Replace(body, `"remember":false`, `"remember":true`, 1)
	}
	w := f.request("POST", "/api/v1/profiles", body, cookie, session.CSRFToken, nil)
	requireStatus(t, w, 201)
	if strings.Contains(w.Body.String(), "private-fixture") {
		t.Fatal("profile creation returned credentials")
	}
	var p profiles.Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *authFixture) createToken(t *testing.T, cookie *http.Cookie, session auth.Session, id string, scopes []string) (auth.Token, string) {
	t.Helper()
	body, err := json.Marshal(auth.TokenCreate{Name: "Synthetic token", ProfileIDs: []string{id}, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("POST", "/api/v1/tokens", string(body), cookie, session.CSRFToken, nil)
	requireStatus(t, w, 201)
	var result struct {
		Token  auth.Token `json:"token"`
		Secret string     `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Secret == "" {
		t.Fatal("token creation omitted secret")
	}
	return result.Token, result.Secret
}

func TestProfileAndTokenHTTPPermissions(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	one := f.createProfile(t, cookie, session, true)
	two := f.createProfile(t, cookie, session, true)
	_, raw := f.createToken(t, cookie, session, one.ID, []string{"browse:read", "credentials:unlock"})
	headers := map[string]string{"Authorization": "Bearer " + raw, "Origin": "https://script.example.test"}
	w := f.request("GET", "/api/v1/profiles", "", nil, "", headers)
	requireStatus(t, w, 200)
	var listed struct {
		Profiles []profiles.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Profiles) != 1 || listed.Profiles[0].ID != one.ID {
		t.Fatal("profile list crossed token boundary")
	}
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+one.ID, "", nil, "", headers), 200)
	requireStatus(t, f.request("GET", "/api/v1/profiles/"+two.ID, "", nil, "", headers), 404)
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+two.ID+"/unlock", `{"username":"x","password":"y"}`, nil, "", headers), 404)
	for _, tc := range []struct{ method, path, body string }{{"GET", "/api/v1/tokens", ""}, {"POST", "/api/v1/tokens", `{}`}, {"DELETE", "/api/v1/tokens/unknown", ""}, {"POST", "/api/v1/profiles", `{}`}, {"PATCH", "/api/v1/profiles/" + one.ID, `{"name":"Forbidden edit"}`}, {"DELETE", "/api/v1/profiles/" + one.ID, ""}} {
		requireStatus(t, f.request(tc.method, tc.path, tc.body, nil, "", headers), 403)
	}
	_, readOnly := f.createToken(t, cookie, session, one.ID, []string{"browse:read"})
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+one.ID+"/unlock", `{"username":"x","password":"y"}`, nil, "", map[string]string{"Authorization": "Bearer " + readOnly}), 403)
	for _, path := range []string{"/api/v1/profiles", "/api/v1/profiles/" + one.ID, "/api/v1/tokens"} {
		w := f.request("GET", path, "", cookie, "", nil)
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "private-fixture") || strings.Contains(w.Body.String(), raw) || strings.Contains(w.Body.String(), readOnly) || strings.Contains(w.Body.String(), `"username"`) || strings.Contains(w.Body.String(), `"password"`) {
			t.Fatal("API read exposed credential or token secret")
		}
	}
	requireStatus(t, f.request("PATCH", "/api/v1/profiles/"+one.ID, `{"credentials":{"username":"x","password":"y","unexpected":true}}`, cookie, session.CSRFToken, nil), 400)
}

func TestLogoutAndTokenRevocationCancelTemporaryUpstreamAccess(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	profile := f.createProfile(t, cookie, session, false)
	principal, err := f.server.Auth.Authenticate(context.Background(), cookie.Value, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, ownerActive, ownerCancel, err := f.server.Profiles.Access(context.Background(), profile.ID, profiles.LeaseID(principal))
	if err != nil {
		t.Fatal(err)
	}
	defer ownerCancel()
	token, raw := f.createToken(t, cookie, session, profile.ID, []string{"credentials:unlock"})
	headers := map[string]string{"Authorization": "Bearer " + raw}
	w := f.request("POST", "/api/v1/profiles/"+profile.ID+"/unlock", `{"username":"token-user","password":"token-secret","lease_seconds":60}`, nil, "", headers)
	requireStatus(t, w, 200)
	_, _, tokenActive, tokenCancel, err := f.server.Profiles.Access(context.Background(), profile.ID, "token:"+token.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer tokenCancel()
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/lock", "", nil, "", headers), 204)
	requireAccessCancelled(t, tokenActive)
	if ownerActive.Err() != nil {
		t.Fatal("token lock cancelled owner access")
	}
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/unlock", `{"username":"token-user","password":"token-secret"}`, nil, "", headers), 200)
	_, _, tokenActive, tokenCancel, err = f.server.Profiles.Access(context.Background(), profile.ID, "token:"+token.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer tokenCancel()
	requireStatus(t, f.request("DELETE", "/api/v1/tokens/"+token.ID, "", cookie, session.CSRFToken, nil), 204)
	requireAccessCancelled(t, tokenActive)
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/unlock", `{"username":"x","password":"y"}`, nil, "", headers), 401)
	requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, nil), 204)
	requireAccessCancelled(t, ownerActive)
}

func TestStrictJSONAndBodyLimits(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `null`, `[]`, strings.Repeat("x", 70<<10)} {
		w := f.request("POST", "/api/v1/profiles", body, cookie, session.CSRFToken, nil)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", f.origin+"/api/v1/profiles", io.NopCloser(strings.NewReader(`{}`)))
	r.Header.Set("Origin", f.origin)
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	requireStatus(t, w, 415)
	requireStatus(t, f.request("POST", "/api/v1/profiles", `{}`, cookie, session.CSRFToken, map[string]string{"Content-Type": "application/jsonBAD"}), 415)
}

type diagnosticFetcher func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)

func (f diagnosticFetcher) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	return f(ctx, root, method, path, options)
}

type diagnosticBody struct {
	io.Reader
	closed bool
}

func (b *diagnosticBody) Close() error { b.closed = true; return nil }

func TestProfileDiagnosticsUseCheckedTransportAndRedactFailures(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	profile := f.createProfile(t, cookie, session, true)
	const emptyListing = `<h1>Index of /root/</h1><table><tr><th>Name</th><th>Last modified</th><th>Size</th></tr></table>`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   int
		code   string
	}{
		{"wrong credentials", 401, "private-fixture-user private-fixture-password", nil, 502, "upstream_auth_required"},
		{"forbidden credentials", 403, "private-fixture-user private-fixture-password", nil, 502, "upstream_auth_required"},
		{"source failure", 500, "private source failure details", nil, 502, "upstream_unavailable"},
		{"login page", 200, `<html><form><input type="password"></form>private source failure details</html>`, nil, 502, "unsupported_listing"},
		{"TLS failure", 0, "", upstream.ErrNetwork, 502, "upstream_unavailable"},
		{"supported empty listing", 200, emptyListing, nil, 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &diagnosticBody{Reader: strings.NewReader(tc.body)}
			var called bool
			var upstreamContext context.Context
			f.server.Upstream = diagnosticFetcher(func(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
				called = true
				upstreamContext = ctx
				if method != http.MethodGet || path != "" || !options.Interactive || root.URL().String() != profile.RootURL || options.Credentials == nil || options.Credentials.Username != "private-fixture-user" || options.Credentials.Password != "private-fixture-password" {
					t.Error("diagnostics did not use profile-scoped checked transport inputs")
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: body, Header: http.Header{"Www-Authenticate": []string{`Basic realm="private-fixture-user"`}, "Set-Cookie": []string{"private-source-cookie=secret"}}, Request: &http.Request{URL: root.URL()}}, nil
			})
			w := f.request("POST", "/api/v1/profiles/"+profile.ID+"/test", "", cookie, session.CSRFToken, nil)
			requireStatus(t, w, tc.want)
			if !called {
				t.Fatal("diagnostics bypassed checked transport")
			}
			requireAccessCancelled(t, upstreamContext)
			if tc.err == nil && !body.closed {
				t.Fatal("diagnostics leaked source response body")
			}
			if w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("source headers escaped into app response")
			}
			for _, secret := range []string{"private-fixture-user", "private-fixture-password", "private source failure details", "private-source-cookie"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("source secret escaped into API response")
				}
			}
			var result map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if tc.code != "" && result["code"] != tc.code {
				t.Fatalf("code=%s want=%s", result["code"], tc.code)
			}
			if tc.want == 200 && (result["status"] != "ready" || result["listing_format"] != "apache_table" || result["canonical_root_url"] != profile.RootURL || result["range_observation"] != "not_tested") {
				t.Fatal("invalid successful diagnostic response", result)
			}
		})
	}
	_, raw := f.createToken(t, cookie, session, profile.ID, []string{"browse:read", "content:read", "credentials:unlock"})
	f.server.Upstream = diagnosticFetcher(func(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error) {
		t.Error("non-owner diagnostic request reached upstream")
		return nil, upstream.ErrNetwork
	})
	requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/test", "", nil, "", map[string]string{"Authorization": "Bearer " + raw}), 403)
	requireStatus(t, f.request("GET", "/api/v1/auth/session", "", cookie, "", map[string]string{"Authorization": "Bearer " + raw}), 403)
}

func TestUnlockInputAndTokenExpiryBounds(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	profile := f.createProfile(t, cookie, session, false)
	for _, body := range []string{`{}`, `{"username":"x"}`, `{"password":"y"}`, `{"username":"x","password":"y","lease_seconds":0}`, `{"username":"x","password":"y","lease_seconds":59}`, `{"username":"x","password":"y","lease_seconds":28801}`, `{"username":"x","password":"y","remember":true}`} {
		t.Run(body, func(t *testing.T) {
			requireStatus(t, f.request("POST", "/api/v1/profiles/"+profile.ID+"/unlock", body, cookie, session.CSRFToken, nil), 400)
		})
	}
	expires := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	_, raw, err := f.server.Auth.CreateToken(context.Background(), auth.TokenCreate{Name: "Expiring token", ProfileIDs: []string{profile.ID}, Scopes: []string{"credentials:unlock"}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("POST", "/api/v1/profiles/"+profile.ID+"/unlock", `{"username":"x","password":"y","lease_seconds":28800}`, nil, "", map[string]string{"Authorization": "Bearer " + raw})
	requireStatus(t, w, 200)
	var result struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.ExpiresAt.Equal(expires) {
		t.Fatal("credential lease outlives API token")
	}
}
