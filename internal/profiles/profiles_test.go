package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/store"
)

func profileFixture(t *testing.T) (*Service, auth.Principal) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db.DB, vault)
	t.Cleanup(func() { s.Close(); db.Close() })
	return s, auth.Principal{Owner: true, CreatorID: "owner", SessionID: "owner-session", ExpiresAt: time.Now().Add(time.Hour)}
}

func createBasic(t *testing.T, s *Service, p auth.Principal, remember bool) Profile {
	t.Helper()
	profile, err := s.Create(context.Background(), Input{Name: "Synthetic source", RootURL: "https://files.example.test/root/", AuthType: "basic", Credentials: &Credentials{Username: "synthetic-user", Password: "synthetic-secret", Remember: remember}}, p)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func requireCancelled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active access was not cancelled")
	}
}

func TestTemporaryCredentialsAreIsolatedAndNeverPersisted(t *testing.T) {
	s, owner := profileFixture(t)
	ctx := context.Background()
	profile := createBasic(t, s, owner, false)
	if profile.CredentialState != "temporary" {
		t.Fatal(profile.CredentialState)
	}
	_, credential, active, cancel, err := s.Access(ctx, profile.ID, LeaseID(owner))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if credential.Username != "synthetic-user" || credential.Password != "synthetic-secret" {
		t.Fatal("temporary credentials unavailable")
	}
	for _, lease := range []string{"session:other-session", "token:other-token"} {
		if _, _, _, _, err := s.Access(ctx, profile.ID, lease); !errors.Is(err, ErrLocked) {
			t.Fatal("credential crossed principal boundary", err)
		}
		other, err := s.Get(ctx, profile.ID, lease)
		if err != nil || other.CredentialState != "locked" {
			t.Fatal(other, err)
		}
	}
	var settings string
	var stored []byte
	if err := s.db.QueryRow(`SELECT settings,credentials FROM profiles WHERE id=?`, profile.ID).Scan(&settings, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 || strings.Contains(settings, "synthetic-user") || strings.Contains(settings, "synthetic-secret") {
		t.Fatal("temporary credentials persisted")
	}
	second := New(s.db, s.vault)
	defer second.Close()
	if _, _, _, _, err := second.Access(ctx, profile.ID, LeaseID(owner)); !errors.Is(err, ErrLocked) {
		t.Fatal("new service recovered temporary credentials", err)
	}
	s.ClearPrincipal(LeaseID(owner))
	requireCancelled(t, active)
	if _, _, _, _, err := s.Access(ctx, profile.ID, LeaseID(owner)); !errors.Is(err, ErrLocked) {
		t.Fatal("logout retained temporary credentials", err)
	}
}

func TestLeaseExpiryLockReplacementAndClose(t *testing.T) {
	s, owner := profileFixture(t)
	ctx := context.Background()
	profile := createBasic(t, s, owner, false)
	tokenLease := "token:synthetic-token"
	if err := s.Unlock(profile, tokenLease, Credentials{Username: "token-user", Password: "token-secret"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, tokenCredential, tokenActive, tokenCancel, err := s.Access(ctx, profile.ID, tokenLease)
	if err != nil {
		t.Fatal(err)
	}
	defer tokenCancel()
	if tokenCredential.Username != "token-user" {
		t.Fatal("token did not receive its own credentials")
	}
	_, ownerCredential, ownerActive, ownerCancel, err := s.Access(ctx, profile.ID, LeaseID(owner))
	if err != nil {
		t.Fatal(err)
	}
	defer ownerCancel()
	if ownerCredential.Username != "synthetic-user" {
		t.Fatal("token overwrote owner credentials")
	}
	s.Lock(profile.ID, tokenLease)
	requireCancelled(t, tokenActive)
	if ownerActive.Err() != nil {
		t.Fatal("token lock cancelled owner lease")
	}
	if err := s.Unlock(profile, tokenLease, Credentials{Username: "short-user", Password: "short-secret"}, time.Now().Add(80*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, _, shortActive, shortCancel, err := s.Access(ctx, profile.ID, tokenLease)
	if err != nil {
		t.Fatal(err)
	}
	defer shortCancel()
	requireCancelled(t, shortActive)
	if _, _, _, _, err := s.Access(ctx, profile.ID, tokenLease); !errors.Is(err, ErrLocked) {
		t.Fatal("expired lease accepted", err)
	}
	if err := s.Unlock(profile, LeaseID(owner), Credentials{Username: "replacement", Password: "new-secret"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	requireCancelled(t, ownerActive)
	_, _, replacement, replacementCancel, err := s.Access(ctx, profile.ID, LeaseID(owner))
	if err != nil {
		t.Fatal(err)
	}
	defer replacementCancel()
	s.Close()
	requireCancelled(t, replacement)
}

func TestRememberedCredentialsEncryptedAndProfileBound(t *testing.T) {
	s, owner := profileFixture(t)
	ctx := context.Background()
	one := createBasic(t, s, owner, true)
	two := createBasic(t, s, owner, true)
	if one.CredentialState != "remembered" {
		t.Fatal(one.CredentialState)
	}
	var settings string
	var encrypted []byte
	if err := s.db.QueryRow(`SELECT settings,credentials FROM profiles WHERE id=?`, one.ID).Scan(&settings, &encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) == 0 || bytes.Contains(encrypted, []byte("synthetic-secret")) || bytes.Contains(encrypted, []byte("synthetic-user")) || strings.Contains(settings, "synthetic-secret") || strings.Contains(settings, "synthetic-user") {
		t.Fatal("remembered credentials were not protected")
	}
	for _, service := range []*Service{s, New(s.db, s.vault)} {
		_, credentials, active, cancel, err := service.Access(ctx, one.ID, "token:authorized-token")
		if err != nil {
			t.Fatal(err)
		}
		if credentials.Username != "synthetic-user" || credentials.Password != "synthetic-secret" {
			t.Fatal("remembered credentials did not survive service recreation")
		}
		service.ClearPrincipal("token:authorized-token")
		if active.Err() != nil {
			t.Fatal("clearing temporary credentials cancelled remembered access")
		}
		cancel()
		if service != s {
			service.Close()
		}
	}
	if _, err := s.db.Exec(`UPDATE profiles SET credentials=? WHERE id=?`, encrypted, two.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.Access(ctx, two.ID, LeaseID(owner)); !errors.Is(err, ErrLocked) {
		t.Fatal("cross-profile ciphertext accepted", err)
	}
	wrong, err := auth.CreateVault(filepath.Join(t.TempDir(), "wrong.key"))
	if err != nil {
		t.Fatal(err)
	}
	wrongService := New(s.db, wrong)
	defer wrongService.Close()
	if _, _, _, _, err := wrongService.Access(ctx, one.ID, LeaseID(owner)); !errors.Is(err, ErrLocked) {
		t.Fatal("wrong vault key accepted", err)
	}
}

func TestSourcePatchInvalidatesCredentialsAndActiveWork(t *testing.T) {
	s, owner := profileFixture(t)
	ctx := context.Background()
	profile := createBasic(t, s, owner, true)
	_, _, remembered, rememberedCancel, err := s.Access(ctx, profile.ID, "token:remembered")
	if err != nil {
		t.Fatal(err)
	}
	defer rememberedCancel()
	if err := s.Unlock(profile, LeaseID(owner), Credentials{Username: "temporary", Password: "temporary-secret"}, owner.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	_, _, temporary, temporaryCancel, err := s.Access(ctx, profile.ID, LeaseID(owner))
	if err != nil {
		t.Fatal(err)
	}
	defer temporaryCancel()
	patched, err := s.Patch(ctx, profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://other.example.test/new-root/"`)}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Generation != profile.Generation+1 || patched.CredentialState != "locked" {
		t.Fatal(patched)
	}
	requireCancelled(t, remembered)
	requireCancelled(t, temporary)
	if _, _, _, _, err := s.Access(ctx, profile.ID, LeaseID(owner)); !errors.Is(err, ErrLocked) {
		t.Fatal("source change retained credentials", err)
	}
	var encrypted []byte
	if err := s.db.QueryRow(`SELECT credentials FROM profiles WHERE id=?`, profile.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) > 0 {
		t.Fatal("source change retained remembered credentials")
	}
}

func TestMetadataPatchKeepsCredentialsAndDeleteCancelsWork(t *testing.T) {
	s, owner := profileFixture(t)
	ctx := context.Background()
	profile := createBasic(t, s, owner, true)
	_, _, active, cancel, err := s.Access(ctx, profile.ID, LeaseID(owner))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	patched, err := s.Patch(ctx, profile.ID, map[string]json.RawMessage{"name": json.RawMessage(`"Renamed fixture"`)}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Generation != profile.Generation || patched.CredentialState != "remembered" || active.Err() != nil {
		t.Fatal("metadata patch invalidated source access")
	}
	if err := s.Delete(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	requireCancelled(t, active)
	if _, err := s.Get(ctx, profile.ID, LeaseID(owner)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
