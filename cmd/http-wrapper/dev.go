package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
)

func configureDev(cfg *config.Config) error {
	host, _, err := net.SplitHostPort(cfg.Address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dev requires a literal loopback listen address, such as 127.0.0.1:8080")
	}
	if cfg.PublicURL == nil {
		return fmt.Errorf("dev requires a loopback public URL")
	}
	host = cfg.PublicURL.Hostname()
	ip = net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("dev requires localhost or a loopback IP in HTTPWRAPPER_PUBLIC_URL")
	}
	cfg.DataDir = filepath.Join(".local", "dev", "data")
	cfg.KeysDir = filepath.Join(".local", "dev", "keys")
	cfg.DownloadsDir = filepath.Join(".local", "dev", "downloads")
	cfg.CacheDir = filepath.Join(".local", "dev", "cache")
	cfg.MediaSocket = ""
	return nil
}

// Development uses a normal owner session so authorization and CSRF checks still run.
func devSession(ctx context.Context, cfg config.Config, db *store.Store, owner *auth.Service) (string, error) {
	var owners, checks int
	if err := db.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM owner_auth").Scan(&owners); err != nil {
		return "", err
	}
	if err := db.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE key='credential_key_check'").Scan(&checks); err != nil {
		return "", err
	}
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	vault, err := auth.LoadVault(keyPath)
	if err != nil {
		if _, statErr := os.Lstat(keyPath); !errors.Is(statErr, os.ErrNotExist) || owners != 0 || checks != 0 {
			return "", err
		}
		if err = os.MkdirAll(cfg.KeysDir, 0700); err != nil {
			return "", err
		}
		vault, err = auth.CreateVault(keyPath)
		if err != nil {
			return "", err
		}
	}
	if checks == 0 && owners == 0 {
		err = auth.InitializeKeyCheck(ctx, db.DB, vault)
	} else {
		err = auth.VerifyKey(ctx, db.DB, vault)
	}
	if err != nil {
		return "", err
	}
	secretPath := filepath.Join(cfg.KeysDir, "owner.secret")
	secret, err := os.ReadFile(secretPath)
	if errors.Is(err, os.ErrNotExist) && owners == 0 {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			return "", err
		}
		secret = []byte(base64.RawURLEncoding.EncodeToString(raw))
		f, e := os.OpenFile(secretPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", e
		}
		_, err = f.Write(secret)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return "", fmt.Errorf("development owner secret is unavailable")
	}
	defer clear(secret)
	if owners == 0 {
		if err = owner.Setup(ctx, string(secret)); err != nil {
			return "", err
		}
	}
	cookie, _, err := owner.Login(ctx, string(secret))
	return cookie, err
}

func devHandler(next http.Handler, session, origin string) http.Handler {
	u, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || r.Host != u.Host ||
			(r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Development access is limited to this computer and origin.", http.StatusForbidden)
			return
		}
		// Replace only the app cookie; unrelated cookies and explicit API tokens retain their behavior.
		cookies := r.Cookies()
		r.Header.Del("Cookie")
		for _, cookie := range cookies {
			if cookie.Name != "http_wrapper_session" {
				r.AddCookie(cookie)
			}
		}
		r.AddCookie(&http.Cookie{Name: "http_wrapper_session", Value: session})
		next.ServeHTTP(w, r)
	})
}
