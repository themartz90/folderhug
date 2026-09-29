package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRefreshReloadsTokenAuthority(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	token, raw, err := s.CreateToken(ctx, TokenCreate{Name: "events", ProfileIDs: []string{"one", "two"}, Scopes: []string{"browse:read", "downloads:write"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE api_tokens SET profile_ids='["two"]',scopes='["browse:read"]' WHERE id=?`, token.ID); err != nil {
		t.Fatal(err)
	}
	p, err = s.Refresh(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Can("one", "browse:read") || p.Can("two", "downloads:write") || !p.Can("two", "browse:read") {
		t.Fatal("refresh kept stale authority")
	}
	if _, err := s.db.Exec(`UPDATE api_tokens SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("refresh accepted expiry", err)
	}
	if _, err := s.db.Exec(`UPDATE api_tokens SET expires_at=NULL WHERE id=?`, token.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("refresh accepted revoked token", err)
	}
}

func TestRefreshRequiresLiveSessionAndRejectsForgedOwner(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.Setup(ctx, testPassword); err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.Refresh(ctx, p)
	if err != nil || !refreshed.Owner || refreshed.SessionID != p.SessionID {
		t.Fatal(refreshed, err)
	}
	for _, forged := range []Principal{{}, {Owner: true, CreatorID: "owner"}, {Owner: true, CreatorID: "token", SessionID: p.SessionID}, {CreatorID: "owner", SessionID: p.SessionID}, {Owner: true, CreatorID: "owner", SessionID: "missing"}} {
		if _, err := s.Refresh(ctx, forged); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal("invalid principal accepted", err)
		}
	}
	if err := s.RevokeSession(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked session accepted", err)
	}
	raw, _, err = s.Login(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE auth_sessions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired session accepted", err)
	}
}
