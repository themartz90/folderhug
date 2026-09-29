package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
)

func TestSetupRecoveryPreservesKeyAndRevokesAccess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{KeysDir: filepath.Join(dir, "keys")}
	db, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := auth.New(db.DB)
	configure := func(command, password string) error {
		input, err := os.CreateTemp(t.TempDir(), "stdin")
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		if _, err = input.WriteString(password + "\n"); err != nil {
			t.Fatal(err)
		}
		if _, err = input.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		original := os.Stdin
		os.Stdin = input
		defer func() { os.Stdin = original }()
		return configureOwner(ctx, command, []string{"--password-stdin"}, cfg, db, owner)
	}
	if err = configure("setup", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = configure("setup", "synthetic-password-2"); !errors.Is(err, auth.ErrAlreadySetup) {
		t.Fatalf("repeat setup: %v", err)
	}
	cookie, _, err := owner.Login(ctx, "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = configure("recover", "synthetic-password-2"); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Authenticate(ctx, cookie, ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("old session survived recovery")
	}
	if _, _, err = owner.Login(ctx, "synthetic-password-2"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(key) != string(after) {
		t.Fatal("recovery replaced credential key")
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err = configure("recover", "synthetic-password-3"); !errors.Is(err, auth.ErrKey) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err = os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery recreated missing key")
	}
}
