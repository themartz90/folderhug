package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/store"
)

func TestDevConfigRestrictsNetworkAndSeparatesData(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8080", ":8080", "192.168.1.2:8080", "localhost:8080"} {
		u, _ := url.Parse("http://localhost:8080")
		cfg := config.Config{Address: address, PublicURL: u}
		if configureDev(&cfg) == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	for _, origin := range []string{"http://example.com", "http://192.168.1.2:8080"} {
		u, _ := url.Parse(origin)
		cfg := config.Config{Address: "127.0.0.1:8080", PublicURL: u}
		if configureDev(&cfg) == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	u, _ := url.Parse("http://127.0.0.1:5173")
	cfg := config.Config{Address: "127.0.0.1:8080", PublicURL: u, DataDir: "production"}
	if err := configureDev(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != filepath.Join(".local", "dev", "data") {
		t.Fatal("development data not isolated")
	}
}

func TestDevSessionPreservesKeyAndFailsWhenLost(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{KeysDir: filepath.Join(root, "keys")}
	db, err := store.Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := auth.New(db.DB)
	cookie, err := devSession(ctx, cfg, db, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Authenticate(ctx, cookie, ""); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(cfg.KeysDir, "credentials.key")
	key, _ := os.ReadFile(keyPath)
	if _, err = devSession(ctx, cfg, db, owner); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(keyPath)
	if string(key) != string(after) {
		t.Fatal("key replaced")
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err = devSession(ctx, cfg, db, owner); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("lost key recreated")
	}
}

func TestDevHandlerRejectsRemoteAndCrossOrigin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("http_wrapper_session")
		if err != nil || c.Value != "local-session" {
			t.Error("missing development session")
		}
		w.WriteHeader(204)
	})
	h := devHandler(next, "local-session", "http://localhost:8080")
	for _, tc := range []struct {
		remote, origin, host string
		status               int
	}{
		{"127.0.0.1:1234", "", "localhost:8080", 204},
		{"[::1]:1234", "http://localhost:8080", "localhost:8080", 204},
		{"192.168.1.2:1234", "", "localhost:8080", 403},
		{"127.0.0.1:1234", "http://evil.example", "localhost:8080", 403},
		{"127.0.0.1:1234", "", "evil.example", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/v1/auth/session", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
}
