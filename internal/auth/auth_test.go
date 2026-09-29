package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db.DB)
}

const testPassword = "a synthetic password"

func TestCredentialInstallationSerializesWithRevocation(t *testing.T) {
	s := newTestService(t)
	if err := s.Setup(t.Context(), testPassword); err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.Login(t.Context(), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(t.Context(), raw, "")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	installed := make(chan error, 1)
	go func() {
		installed <- s.WithLivePrincipal(t.Context(), p, func(Principal) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- s.Logout(t.Context(), raw) }()
	select {
	case err := <-revoked:
		close(release)
		t.Fatalf("revocation passed an in-flight installation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err = <-installed; err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	err = s.WithLivePrincipal(t.Context(), p, func(Principal) error {
		t.Error("revoked principal installed credentials")
		return nil
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestOwnerSessionLifecycle(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.Setup(ctx, "too short"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Setup(ctx, strings.Repeat("a", 1025)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(ctx, "other synthetic password"); !errors.Is(err, ErrAlreadySetup) {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "wrong password"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	raw, session, err := s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !session.Authenticated || session.CSRFToken == "" || !session.ExpiresAt.After(time.Now()) {
		t.Fatal("invalid session")
	}
	var storedPassword string
	var storedSession []byte
	if err := s.db.QueryRow(`SELECT password_hash FROM owner_auth`).Scan(&storedPassword); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedPassword, testPassword) || !strings.HasPrefix(storedPassword, "$argon2id$") {
		t.Fatal("password was not hashed")
	}
	if err := s.db.QueryRow(`SELECT secret_hash FROM auth_sessions`).Scan(&storedSession); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(storedSession, []byte(raw)) || len(storedSession) != 32 {
		t.Fatal("session was not hashed")
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil || !p.Owner || p.CreatorID != "owner" || p.SessionID == "" || p.Session() != session {
		t.Fatalf("owner session: %+v %v", p, err)
	}
	if !p.Can("other", "downloads:write") || !p.CanJob("", "another token", "downloads:write") {
		t.Fatal("owner authority missing")
	}
	if _, err := s.Authenticate(ctx, raw, "invalid bearer"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("bearer fallback", err)
	}
	if err := s.Logout(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("logout failed", err)
	}
	raw, _, err = s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE auth_sessions SET expires_at=?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("session expiry failed", err)
	}
}

func TestMalformedPasswordHashesFailClosed(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	var validHash string
	if err := s.db.QueryRow(`SELECT password_hash FROM owner_auth`).Scan(&validHash); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(validHash, "$")
	parts[len(parts)-1] = strings.Repeat("A", 43)
	tamperedHash := strings.Join(parts, "$")
	for _, hash := range []string{"", "$argon2i$v=19$m=65536,t=3,p=2$a$b", "$argon2id$v=19$m=4294967295,t=3,p=2$a$b", passwordPrefix + "bad$hash", passwordPrefix + "$$$$"} {
		if _, err := s.db.Exec(`UPDATE owner_auth SET password_hash=?`, hash); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Login(ctx, testPassword); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal("accepted malformed hash", err)
		}
	}
	if _, err := s.db.Exec(`UPDATE owner_auth SET password_hash=?`, tamperedHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, testPassword); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("accepted tampered password hash", err)
	}
}

func TestEveryTokenScopeIsIndependent(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	scopes := []string{"browse:read", "content:read", "scan:write", "downloads:write", "credentials:unlock"}
	for _, scope := range scopes {
		t.Run(scope, func(t *testing.T) {
			token, raw, err := s.CreateToken(ctx, TokenCreate{Name: "fixture", ProfileIDs: []string{"one"}, Scopes: []string{scope}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.Authenticate(ctx, "", raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, requested := range scopes {
				if p.Can("one", requested) != (scope == requested) || p.Can("two", requested) {
					t.Fatalf("%s escaped scope/profile restriction for %s", scope, requested)
				}
			}
			if !p.CanJob("one", token.ID, scope) || p.CanJob("one", "other-token", scope) {
				t.Fatal("job ownership escaped")
			}
			if err := s.DeleteToken(ctx, token.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Authenticate(ctx, "", raw); !errors.Is(err, ErrUnauthenticated) {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartAndExplicitSessionRevocation(t *testing.T) {
	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := New(db.DB)
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = New(db.DB)
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestTokenPermissionsExpiryAndRevocation(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour)
	token, raw, err := s.CreateToken(ctx, TokenCreate{Name: "fixture", ProfileIDs: []string{"one"}, Scopes: []string{"browse:read", "downloads:write"}, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT secret_hash FROM api_tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, []byte(raw)) || len(stored) != 32 {
		t.Fatal("token was not hashed")
	}
	p, err := s.Authenticate(ctx, "", raw)
	if err != nil || p.Owner || p.CreatorID != token.ID {
		t.Fatal(p, err)
	}
	for _, tc := range []struct {
		profile, scope string
		allow          bool
	}{{"one", "browse:read", true}, {"one", "downloads:write", true}, {"two", "browse:read", false}, {"one", "credentials:unlock", false}, {"one", "content:read", false}, {"one", "admin", false}, {"", "browse:read", false}} {
		if got := p.Can(tc.profile, tc.scope); got != tc.allow {
			t.Fatalf("%+v: got %v", tc, got)
		}
	}
	if !p.CanJob("one", token.ID, "downloads:write") || p.CanJob("one", "other", "downloads:write") || p.CanJob("two", token.ID, "downloads:write") {
		t.Fatal("job ownership escaped")
	}
	tokens, err := s.ListTokens(ctx)
	if err != nil || len(tokens) != 1 || tokens[0].ID != token.ID {
		t.Fatal(tokens, err)
	}
	if _, err = s.db.Exec(`UPDATE api_tokens SET expires_at=?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "", raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired token accepted", err)
	}
	if err := s.DeleteToken(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "", raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked token accepted", err)
	}
	for _, input := range []TokenCreate{{Name: "x", ProfileIDs: []string{"one"}, Scopes: []string{"admin"}}, {Name: "x", ProfileIDs: []string{"one", "one"}, Scopes: []string{"browse:read"}}, {Name: "x", Scopes: []string{"browse:read"}}, {Name: "x", ProfileIDs: []string{"one"}, Scopes: []string{"browse:read"}, ExpiresAt: new(time.Time)}} {
		if _, _, err := s.CreateToken(ctx, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid token accepted", err)
		}
	}
}

func TestRecoveryRevokesCredentialsAndPreservesOwner(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.Recover(ctx, testPassword); !errors.Is(err, ErrNotSetup) {
		t.Fatal(err)
	}
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	cookie, _, err := s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.CreateToken(ctx, TokenCreate{Name: "fixture", ProfileIDs: []string{"one"}, Scopes: []string{"browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx, "replacement synthetic password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, cookie, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "", raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, testPassword); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "replacement synthetic password"); err != nil {
		t.Fatal(err)
	}
}
