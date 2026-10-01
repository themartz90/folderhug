package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/themartz90/folderhug/internal/profiles"
)

func TestPendingTokenCreationRejectsRevokedSession(t *testing.T) {
	for _, revoke := range []string{"logout", "logout-all", "recovery", "expiry"} {
		t.Run(revoke, func(t *testing.T) {
			f := newAuthFixture(t, "https")
			cookie, session := f.login(t)
			owner, err := f.server.Auth.Authenticate(t.Context(), cookie.Value, "")
			if err != nil {
				t.Fatal(err)
			}
			profile, err := f.server.Profiles.Create(t.Context(), profiles.Input{Name: "Fixture", RootURL: "https://files.example.test/", AuthType: "none"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"name":"pending","profile_ids":["` + profile.ID + `"],"scopes":["browse:read"]}`
			gate := &gatedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(body)}
			req := httptest.NewRequest("POST", f.origin+"/api/v1/tokens", gate)
			req.AddCookie(cookie)
			req.Header.Set("Origin", f.origin)
			req.Header.Set("X-CSRF-Token", session.CSRFToken)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.handler.ServeHTTP(w, req) }()
			awaitGate(t, gate.entered)
			switch revoke {
			case "logout", "logout-all":
				requireStatus(t, f.request("POST", "/api/v1/auth/"+revoke, "", cookie, session.CSRFToken, nil), http.StatusNoContent)
			case "recovery":
				if err := f.server.Auth.Recover(t.Context(), "replacement owner password"); err != nil {
					close(gate.release)
					awaitGate(t, done)
					t.Fatal(err)
				}
			case "expiry":
				if _, err := f.server.Store.DB.ExecContext(t.Context(), `UPDATE auth_sessions SET expires_at=0 WHERE id=?`, owner.SessionID); err != nil {
					close(gate.release)
					awaitGate(t, done)
					t.Fatal(err)
				}
			}
			close(gate.release)
			awaitGate(t, done)
			requireStatus(t, w, http.StatusUnauthorized)
			tokens, err := f.server.Auth.ListTokens(t.Context())
			if err != nil || len(tokens) != 0 {
				t.Fatalf("revoked session created persistent tokens: count=%d err=%v", len(tokens), err)
			}
		})
	}
}
