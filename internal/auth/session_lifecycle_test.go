package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/store"
	_ "modernc.org/sqlite"
)

func TestRememberedSessionRenewalAndRotation(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	raw, created, err := s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	if !created.ExpiresAt.Equal(clock.Add(90 * 24 * time.Hour)) {
		t.Fatal(created.ExpiresAt)
	}
	if !created.CookieExpiresAt.Equal(clock.Add(365 * 24 * time.Hour)) {
		t.Fatal(created.CookieExpiresAt)
	}
	encoded, err := json.Marshal(created)
	if err != nil || strings.Contains(string(encoded), "cookie_expires") || strings.Contains(string(encoded), "CookieExpires") {
		t.Fatal(string(encoded), err)
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(24*time.Hour + time.Second)
	replacement, renewed, err := s.RenewSession(ctx, p, raw)
	if err != nil || replacement == "" || replacement == raw {
		t.Fatalf("rotation: %q %v", replacement, err)
	}
	if renewed.CSRFToken != created.CSRFToken || !renewed.ExpiresAt.Equal(clock.Add(90*24*time.Hour)) {
		t.Fatal(renewed)
	}
	if !renewed.CookieExpiresAt.Equal(created.CookieExpiresAt) {
		t.Fatal("cookie absolute expiry moved", renewed.CookieExpiresAt)
	}
	current, err := s.Authenticate(ctx, replacement, "")
	if err != nil || current.SessionID != p.SessionID {
		t.Fatal(current, err)
	}
	if _, err := s.Authenticate(ctx, raw, ""); err != nil {
		t.Fatal("previous secret in grace", err)
	}
	second, _, err := s.RenewSession(ctx, p, raw)
	if err != nil || second != "" {
		t.Fatalf("stale cookie replaced current: %q %v", second, err)
	}
	clock = clock.Add(2*time.Minute + time.Second)
	if _, err := s.Authenticate(ctx, raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("previous secret survived grace", err)
	}
	if _, err := s.Authenticate(ctx, replacement, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("logout left session live", err)
	}
}

func TestSessionLifetimesAndAbsoluteLimit(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	normalRaw, normal, err := s.Login(ctx, testPassword)
	if err != nil || !normal.ExpiresAt.Equal(clock.Add(30*24*time.Hour)) {
		t.Fatal(normal, err)
	}
	if normal.CookieExpiresAt != normal.ExpiresAt {
		t.Fatal("normal cookie expiry differs from server expiry")
	}
	normalP, err := s.Authenticate(ctx, normalRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(24 * time.Hour)
	if replacement, unchanged, err := s.RenewSession(ctx, normalP, normalRaw); err != nil || replacement != "" || unchanged != normal {
		t.Fatal(replacement, unchanged, err)
	}
	clock = clock.Add(29 * 24 * time.Hour)
	if _, err := s.Refresh(ctx, normalP); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("normal session refreshed", err)
	}
	clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw, _, err := s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	for days := 89; days < 365; days += 89 {
		clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(days) * 24 * time.Hour)
		replacement, session, err := s.RenewSession(ctx, p, raw)
		if err != nil {
			t.Fatal(err)
		}
		if replacement != "" {
			raw = replacement
		}
		if session.ExpiresAt.After(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(365 * 24 * time.Hour)) {
			t.Fatal("absolute cap exceeded")
		}
	}
	clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(365 * 24 * time.Hour)
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("absolute expiry bypassed", err)
	}
}

func TestConcurrentRememberedRenewalsOnlyReplaceOnce(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(25 * time.Hour)
	type result struct {
		raw string
		err error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replacement, _, err := s.RenewSession(ctx, p, raw)
			results <- result{replacement, err}
		}()
	}
	wg.Wait()
	close(results)
	rotations := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.raw != "" {
			rotations++
			if _, err := s.Authenticate(ctx, result.raw, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rotations != 1 {
		t.Fatalf("got %d replacements", rotations)
	}
}

func TestCompletedSetupMarkerBlocksRecreation(t *testing.T) {
	s := newTestService(t)
	if _, err := s.db.Exec("INSERT INTO settings(key,value) VALUES('owner_setup_complete','1')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(context.Background(), testPassword); !errors.Is(err, ErrAlreadySetup) {
		t.Fatal(err)
	}
}

func TestSixUnicodeCharactersAndByteLimit(t *testing.T) {
	s := newTestService(t)
	if err := s.Setup(t.Context(), "äöüßéñ"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(t.Context(), "äöüßéñ"); err != nil {
		t.Fatal(err)
	}
	if validPassword("aaaaa") || validPassword("abc\xffde") || validPassword(strings.Repeat("é", 513)) {
		t.Fatal("invalid password accepted")
	}
}

func TestRememberedIdleExpiryAndRevocation(t *testing.T) {
	s := newTestService(t)
	ctx := t.Context()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(90 * 24 * time.Hour)
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("idle session survived expiry", err)
	}
	if _, _, err := s.RenewSession(ctx, p, raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired session renewed", err)
	}
	clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw, _, err = s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(25 * time.Hour)
	replacement, _, err := s.RenewSession(ctx, p, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, replacement, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("previous cookie logout left current live", err)
	}
	raw, _, err = s.LoginWithRemember(ctx, testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("all sessions revocation failed", err)
	}
}

func TestPopulatedSessionMigrationPreservesLegacySession(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "http-wrapper.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_initial.sql", "002_auth.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "store", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(name) VALUES(?)`, "migrations/"+name); err != nil {
			t.Fatal(err)
		}
	}
	raw := "legacy-session-secret"
	expires := time.Now().Add(7 * 24 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at) VALUES(?,?,?,?)`, "legacy-id", secretHash(raw), "legacy-csrf", expires); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	s := New(upgraded.DB)
	p, err := s.Authenticate(t.Context(), raw, "")
	if err != nil || p.SessionID != "legacy-id" || p.CSRFToken != "legacy-csrf" {
		t.Fatal(p, err)
	}
	var remembered, absolute int64
	if err := upgraded.DB.QueryRow(`SELECT remembered,absolute_expires_at FROM auth_sessions WHERE id='legacy-id'`).Scan(&remembered, &absolute); err != nil {
		t.Fatal(err)
	}
	if remembered != 0 || absolute != expires {
		t.Fatal(remembered, absolute, expires)
	}
}
