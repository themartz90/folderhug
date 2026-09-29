package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

type gatedBody struct {
	entered chan struct{}
	release chan struct{}
	reader  io.Reader
	once    bool
}

func awaitGate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("credential request did not reach the expected boundary")
	}
}

func (g *gatedBody) Read(p []byte) (int, error) {
	if !g.once {
		g.once = true
		close(g.entered)
		<-g.release
	}
	return g.reader.Read(p)
}

type credentialObserver struct {
	root   string
	secret bool
}

func (o *credentialObserver) Do(ctx context.Context, r *upstream.Root, method, path string, opts upstream.Options) (*http.Response, error) {
	o.root = r.URL().String()
	o.secret = opts.Credentials != nil && opts.Credentials.Password == "old-secret"
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("unsupported synthetic listing")), Header: make(http.Header)}, nil
}
func TestUnlockRejectsChangedProfile(t *testing.T) {
	f := newAuthFixture(t, "https")
	cookie, session := f.login(t)
	principal, err := f.server.Auth.Authenticate(context.Background(), cookie.Value, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.server.Profiles.Create(context.Background(), profiles.Input{Name: "original", RootURL: "https://old.example.test/files/", AuthType: "basic"}, principal)
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"username":"old-user","password":"old-secret"}`)}
	req := httptest.NewRequest("POST", f.origin+"/api/v1/profiles/"+p.ID+"/unlock", gate)
	req.AddCookie(cookie)
	req.Header.Set("Origin", f.origin)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.handler.ServeHTTP(w, req) }()
	awaitGate(t, gate.entered)
	_, err = f.server.Profiles.Patch(context.Background(), p.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://new.example.test/files/"`), "auth_type": json.RawMessage(`"none"`)}, principal)
	if err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	awaitGate(t, done)
	current, credentials, _, cancel, err := f.server.Profiles.Access(context.Background(), p.ID, profiles.LeaseID(principal))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if w.Code != 409 || credentials != nil {
		t.Fatalf("stale unlock accepted: status=%d credentials=%v", w.Code, credentials != nil)
	}
	observer := &credentialObserver{}
	f.server.Upstream = observer
	f.request("POST", "/api/v1/profiles/"+p.ID+"/test", "", cookie, session.CSRFToken, nil)
	if observer.root != "https://new.example.test/files/" {
		t.Fatal("connection test did not use the new source")
	}
	if observer.secret {
		t.Fatal("old credentials passed to new source")
	}
	if current.AuthType != "none" {
		t.Fatal("profile mutation did not apply")
	}
}

func TestPendingUnlockRejectsConfigurationAndAuthorityChanges(t *testing.T) {
	for _, change := range []string{"root", "authentication", "credentials", "delete", "logout", "revoke token"} {
		t.Run(change, func(t *testing.T) {
			f := newAuthFixture(t, "https")
			cookie, session := f.login(t)
			owner, err := f.server.Auth.Authenticate(t.Context(), cookie.Value, "")
			if err != nil {
				t.Fatal(err)
			}
			profile, err := f.server.Profiles.Create(t.Context(), profiles.Input{Name: "Fixture", RootURL: "https://files.example.test/", AuthType: "basic"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			principal := owner
			bearer := ""
			tokenID := ""
			if change == "revoke token" {
				token, raw, err := f.server.Auth.CreateToken(t.Context(), auth.TokenCreate{Name: "Fixture", ProfileIDs: []string{profile.ID}, Scopes: []string{"credentials:unlock"}})
				if err != nil {
					t.Fatal(err)
				}
				bearer = raw
				tokenID = token.ID
				principal, err = f.server.Auth.Authenticate(t.Context(), "", raw)
				if err != nil {
					t.Fatal(err)
				}
			}
			gate := &gatedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"username":"old-user","password":"old-secret"}`)}
			req := httptest.NewRequest("POST", f.origin+"/api/v1/profiles/"+profile.ID+"/unlock", gate)
			req.AddCookie(cookie)
			req.Header.Set("Origin", f.origin)
			req.Header.Set("X-CSRF-Token", session.CSRFToken)
			req.Header.Set("Content-Type", "application/json")
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.handler.ServeHTTP(w, req) }()
			awaitGate(t, gate.entered)
			want := 409
			switch change {
			case "root":
				_, err = f.server.Profiles.Patch(t.Context(), profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://new.example.test/"`)}, owner)
			case "authentication":
				_, err = f.server.Profiles.Patch(t.Context(), profile.ID, map[string]json.RawMessage{"auth_type": json.RawMessage(`"none"`)}, owner)
			case "credentials":
				_, err = f.server.Profiles.Patch(t.Context(), profile.ID, map[string]json.RawMessage{"credentials": json.RawMessage(`{"username":"new-user","password":"new-secret"}`)}, owner)
			case "delete":
				err = f.server.Profiles.Delete(t.Context(), profile.ID)
			case "logout":
				want = 401
				requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, nil), 204)
			case "revoke token":
				want = 401
				requireStatus(t, f.request("DELETE", "/api/v1/tokens/"+tokenID, "", cookie, session.CSRFToken, nil), 204)
			}
			close(gate.release)
			awaitGate(t, done)
			if err != nil {
				t.Fatal(err)
			}
			requireStatus(t, w, want)
			_, credential, _, cancel, accessErr := f.server.Profiles.Access(t.Context(), profile.ID, profiles.LeaseID(principal))
			if cancel != nil {
				defer cancel()
			}
			if credential != nil && credential.Password == "old-secret" {
				t.Fatal("stale credential lease was installed")
			}
			if change == "credentials" && (accessErr != nil || credential == nil || credential.Password != "new-secret") {
				t.Fatal("replacement credential lost")
			}
			if change == "delete" && !errors.Is(accessErr, profiles.ErrNotFound) {
				t.Fatal("deleted profile accessible")
			}
			if (change == "logout" || change == "revoke token" || change == "root") && !errors.Is(accessErr, profiles.ErrLocked) {
				t.Fatal("stale lease retained", accessErr)
			}
		})
	}
}

func TestPendingProfileCredentialsRejectLogout(t *testing.T) {
	for _, method := range []string{"POST", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			f := newAuthFixture(t, "https")
			cookie, session := f.login(t)
			principal, err := f.server.Auth.Authenticate(t.Context(), cookie.Value, "")
			if err != nil {
				t.Fatal(err)
			}
			profile, err := f.server.Profiles.Create(t.Context(), profiles.Input{Name: "Fixture", RootURL: "https://files.example.test/", AuthType: "basic"}, principal)
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/profiles"
			body := `{"name":"new","root_url":"https://new.example.test/","auth_type":"basic","credentials":{"username":"user","password":"secret"}}`
			if method == "PATCH" {
				path += "/" + profile.ID
				body = `{"credentials":{"username":"user","password":"secret"}}`
			}
			gate := &gatedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(body)}
			req := httptest.NewRequest(method, f.origin+path, gate)
			req.AddCookie(cookie)
			req.Header.Set("Origin", f.origin)
			req.Header.Set("X-CSRF-Token", session.CSRFToken)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.handler.ServeHTTP(w, req) }()
			awaitGate(t, gate.entered)
			requireStatus(t, f.request("POST", "/api/v1/auth/logout", "", cookie, session.CSRFToken, nil), 204)
			close(gate.release)
			awaitGate(t, done)
			requireStatus(t, w, 401)
			var count int
			if err = f.server.Store.DB.QueryRow("SELECT count(*) FROM profiles").Scan(&count); err != nil || count != 1 {
				t.Fatal("revoked request created profile", count, err)
			}
			current, err := f.server.Profiles.Get(t.Context(), profile.ID, profiles.LeaseID(principal))
			if err != nil || current.CredentialState != "locked" {
				t.Fatal("revoked request installed credentials", err)
			}
		})
	}
}
