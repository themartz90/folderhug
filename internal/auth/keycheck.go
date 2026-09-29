package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
)

const keyCheckText = "HTTP Wrapper credential key v1"

// InitializeKeyCheck records key ownership even before a profile has credentials.
func InitializeKeyCheck(ctx context.Context, db *sql.DB, vault *Vault) error {
	encrypted, err := vault.Seal("__key_check__", []byte(keyCheckText))
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('credential_key_check',?)", base64.StdEncoding.EncodeToString(encrypted))
	return err
}

func VerifyKey(ctx context.Context, db *sql.DB, vault *Vault) error {
	var encoded string
	if err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='credential_key_check'").Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKey
		}
		return err
	}
	cipher, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ErrKey
	}
	plain, err := vault.Open("__key_check__", cipher)
	if err != nil || string(plain) != keyCheckText {
		return ErrKey
	}
	return nil
}
