package appcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
)

func prepareVault(ctx context.Context, cfg config.Config, db *store.Store) (*auth.Vault, error) {
	var owners, complete, pending, checks, other int
	err := db.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM owner_auth),
		(SELECT COUNT(*) FROM settings WHERE key='owner_setup_complete'),
		(SELECT COUNT(*) FROM settings WHERE key='owner_setup_pending'),
		(SELECT COUNT(*) FROM settings WHERE key='credential_key_check'),
		(SELECT COUNT(*) FROM settings WHERE key NOT IN ('owner_setup_complete','owner_setup_pending','credential_key_check')) +
		(SELECT COUNT(*) FROM profiles) + (SELECT COUNT(*) FROM auth_sessions) +
		(SELECT COUNT(*) FROM api_tokens) + (SELECT COUNT(*) FROM download_jobs) +
		(SELECT COUNT(*) FROM scan_jobs) + (SELECT COUNT(*) FROM owner_preferences)`).Scan(&owners, &complete, &pending, &checks, &other)
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	if owners == 0 && (complete != 0 || other != 0 || (checks != 0 && pending == 0)) {
		return nil, fmt.Errorf("owner credentials are missing from an existing installation; restore its database and key")
	}
	if owners == 0 && pending == 0 {
		if _, err = os.Lstat(keyPath); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("credential key exists without matching setup state; restore its database")
		}
		if _, err = db.DB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('owner_setup_pending','1')"); err != nil {
			return nil, err
		}
	}
	if owners == 0 && checks == 0 {
		if err = os.MkdirAll(cfg.KeysDir, 0700); err != nil {
			return nil, err
		}
		var vault *auth.Vault
		if _, err = os.Lstat(keyPath); errors.Is(err, os.ErrNotExist) {
			vault, err = auth.CreateVault(keyPath)
		} else {
			vault, err = auth.LoadVault(keyPath)
		}
		if err != nil {
			return nil, err
		}
		if err = auth.InitializeKeyCheck(ctx, db.DB, vault); err != nil {
			return nil, err
		}
		return vault, nil
	}
	vault, err := auth.LoadVault(keyPath)
	if err != nil {
		return nil, err
	}
	return vault, auth.VerifyKey(ctx, db.DB, vault)
}

func checkMissingDatabase(cfg config.Config) error {
	_, err := os.Lstat(filepath.Join(cfg.DataDir, "http-wrapper.db"))
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, directory := range []string{cfg.DataDir, cfg.KeysDir, cfg.DownloadsDir} {
		if directory == "" {
			continue
		}
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("database is missing but application files remain; restore the matching database and key")
		}
	}
	return nil
}
