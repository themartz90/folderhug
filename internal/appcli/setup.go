package appcli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
	"golang.org/x/term"
)

func configureOwner(ctx context.Context, command string, args []string, cfg config.Config, db *store.Store, owner *auth.Service) error {
	stdin := len(args) == 1 && args[0] == "--password-stdin"
	if len(args) > 0 && !stdin {
		return fmt.Errorf("usage: folderhug %s [--password-stdin]", command)
	}
	var count int
	if err := db.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM owner_auth").Scan(&count); err != nil {
		return err
	}
	if command == "setup" && count != 0 {
		return auth.ErrAlreadySetup
	}
	if command == "recover" && count == 0 {
		return auth.ErrNotSetup
	}
	password, err := readPassword(stdin)
	if err != nil {
		return err
	}
	defer clear(password)
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	var vault *auth.Vault
	if command == "setup" {
		_, err = prepareVault(ctx, cfg, db)
		if err != nil {
			return err
		}
		err = owner.Setup(ctx, string(password))
	} else {
		vault, err = auth.LoadVault(keyPath)
		if err != nil {
			return err
		}
		if err = auth.VerifyKey(ctx, db.DB, vault); err != nil {
			return err
		}
		err = owner.Recover(ctx, string(password))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Owner password saved. Keep the credential key with your database backup.")
	return nil
}

func readPassword(stdin bool) ([]byte, error) {
	if stdin {
		b, err := io.ReadAll(io.LimitReader(bufio.NewReader(os.Stdin), 1027))
		if err != nil || len(b) > 1026 {
			return nil, auth.ErrInvalid
		}
		text := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		clear(b)
		if strings.ContainsAny(text, "\r\n") {
			return nil, auth.ErrInvalid
		}
		return []byte(text), nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("setup needs a terminal; use --password-stdin only with protected input")
	}
	fmt.Fprint(os.Stderr, "Owner password (at least 6 characters): ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("cannot read password")
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	repeat, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	defer clear(repeat)
	if err != nil || string(password) != string(repeat) {
		clear(password)
		return nil, fmt.Errorf("passwords did not match")
	}
	return password, nil
}
