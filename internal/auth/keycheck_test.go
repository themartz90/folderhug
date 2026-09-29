package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/themartz90/folderhug/internal/store"
)

func TestKeyCheckRejectsWrongKeyAndRestores(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keyPath := filepath.Join(t.TempDir(), "key")
	original, err := CreateVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = InitializeKeyCheck(ctx, db.DB, original); err != nil {
		t.Fatal(err)
	}
	wrong, err := CreateVault(filepath.Join(t.TempDir(), "wrong"))
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyKey(ctx, db.DB, wrong); !errors.Is(err, ErrKey) {
		t.Fatalf("wrong key accepted: %v", err)
	}
	restored, err := LoadVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyKey(ctx, db.DB, restored); err != nil {
		t.Fatal(err)
	}
}
