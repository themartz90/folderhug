package appcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
)

func TestFirstRunCreatesKeyAndResumesWithoutReplacingIt(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeysDir: filepath.Join(root, "keys")}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := prepareVault(t.Context(), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(cfg.KeysDir, "credentials.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepareVault(t.Context(), cfg, db); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.KeysDir, "credentials.key"))
	if string(key) != string(after) {
		t.Fatal("bootstrap replaced the key")
	}
	if err = auth.VerifyKey(t.Context(), db.DB, vault); err != nil {
		t.Fatal(err)
	}
}

func TestFirstRunNeverRecreatesMissingInitializedKey(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeysDir: filepath.Join(root, "keys")}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = prepareVault(t.Context(), cfg, db); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err = prepareVault(t.Context(), cfg, db); err == nil {
		t.Fatal("accepted a lost key")
	}
	if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("recreated a lost key")
	}
}

func TestFirstRunNeverReopensAfterClaimOrMissingDatabase(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeysDir: filepath.Join(root, "keys")}
	if err := checkMissingDatabase(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = prepareVault(t.Context(), cfg, db); err != nil {
		t.Fatal(err)
	}
	owner := auth.New(db.DB)
	if err = owner.Setup(t.Context(), "abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec("DELETE FROM owner_auth"); err != nil {
		t.Fatal(err)
	}
	if _, err = prepareVault(t.Context(), cfg, db); err == nil {
		t.Fatal("reopened setup after owner loss")
	}
	cfg.DataDir = filepath.Join(root, "missing-data")
	if err = checkMissingDatabase(cfg); err == nil {
		t.Fatal("accepted a missing database with an existing key")
	}
}

func TestFirstRunResumesAfterKeyWriteBeforeKeyCheck(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeysDir: filepath.Join(root, "keys")}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.DB.Exec("INSERT INTO settings(key,value) VALUES('owner_setup_pending','1')"); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(cfg.KeysDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CreateVault(filepath.Join(cfg.KeysDir, "credentials.key")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg.KeysDir, "credentials.key"))
	if _, err = prepareVault(t.Context(), cfg, db); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.KeysDir, "credentials.key"))
	if string(before) != string(after) {
		t.Fatal("replaced pending key")
	}
}

func TestFirstRunRejectsRecreatedDatabaseWithExistingKey(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{DataDir: filepath.Join(root, "data"), KeysDir: filepath.Join(root, "keys")}
	if err := os.MkdirAll(cfg.KeysDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CreateVault(filepath.Join(cfg.KeysDir, "credentials.key")); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := prepareVault(t.Context(), cfg, db); err == nil {
		t.Fatal("existing key accepted without matching setup state")
	}
	var markers int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM settings").Scan(&markers); err != nil || markers != 0 {
		t.Fatal("rejected bootstrap modified settings", markers, err)
	}
}
