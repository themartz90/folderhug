//go:build webfixture

// web-fixture runs the production services with disposable synthetic data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/httpapi"
	"github.com/themartz90/folderhug/internal/preferences"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/scans"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/updates"
	"github.com/themartz90/folderhug/internal/upstream"
	"github.com/themartz90/folderhug/internal/webui"
)

const ownerPassword = "synthetic owner password"
const controlKey = "synthetic-local-control"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Closing the supervisor pipe also stops the binary on Windows, where
	// terminating a child does not reliably deliver a catchable signal.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); stop() }()
	dir, err := runtimeDirectory()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		return err
	}
	defer db.Close()
	unclaimed := len(os.Args) == 4 && os.Args[3] == "--unclaimed"
	appOrigin := "http://127.0.0.1:4173"
	controlAddress := "127.0.0.1:4174"
	if unclaimed {
		appOrigin = "http://127.0.0.1:4175"
		controlAddress = "127.0.0.1:4176"
	}
	owner := auth.New(db.DB)
	if unclaimed {
		if _, err := db.DB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('owner_setup_pending','1')"); err != nil {
			return err
		}
	} else {
		if err := owner.Setup(ctx, ownerPassword); err != nil {
			return err
		}
	}
	vault, err := auth.CreateVault(filepath.Join(dir, "credentials.key"))
	if err != nil {
		return err
	}
	if err := auth.InitializeKeyCheck(ctx, db.DB, vault); err != nil {
		return err
	}
	profileService := profiles.New(db.DB, vault)
	defer profileService.Close()
	source := newSource()
	sourceServer := httptest.NewServer(source)
	defer sourceServer.Close()
	transport, err := upstream.NewWebFixtureClient(sourceServer.Listener.Addr().String())
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	publicURL, _ := url.Parse(appOrigin)
	cfg := config.Config{Address: publicURL.Host, PublicURL: publicURL, DataDir: dir, DownloadsDir: filepath.Join(dir, "downloads"), CacheDir: filepath.Join(dir, "cache"), StreamMaxBytes: 64 << 20, StreamTimeout: 2 * time.Minute, ThumbnailCacheBytes: 32 << 20}
	app := &httpapi.Server{Config: cfg, Store: db, Auth: owner, Profiles: profileService, Upstream: transport}
	app.BuildInfo = buildinfo.Current()
	app.Updates = updates.New(nil, app.BuildInfo, false)
	defer app.Updates.Close()
	app.Catalog = catalog.New(db.DB, profileService, transport)
	app.Content = content.New(transport, content.Limits{MaxBytes: cfg.StreamMaxBytes, Timeout: cfg.StreamTimeout})
	app.Events = events.New(owner)
	app.Scans = scans.New(db.DB, profileService, owner, app.Catalog, transport, app.Events)
	if err := app.Scans.Start(ctx); err != nil {
		return err
	}
	defer app.Scans.Close()
	if err := os.MkdirAll(cfg.DownloadsDir, 0700); err != nil {
		return err
	}
	files, err := downloadfs.Open(cfg.DownloadsDir)
	if err != nil {
		return err
	}
	defer files.Close()
	app.Downloads = downloads.New(db.DB, profileService, owner, app.Catalog, transport, app.Events, files, downloads.Limits{StorageBudgetBytes: 128 << 20, FreeReserveBytes: -1, UnknownSizeMaxBytes: 64 << 20, RequestTimeout: cfg.StreamTimeout})
	if err := app.Downloads.Start(ctx); err != nil {
		return err
	}
	defer app.Downloads.Close()
	app.Thumbnails, err = thumbnails.New(db.DB, profileService, owner, app.Catalog, transport, app.Events, thumbnails.Config{CacheDir: cfg.CacheDir, MaxBytes: cfg.ThumbnailCacheBytes})
	if err != nil {
		return err
	}
	defer app.Thumbnails.Close()
	app.Preferences = preferences.New(db.DB, profileService, app.Catalog)
	app.StatusProvider = func(context.Context) (preferences.Status, error) {
		return preferences.Status{ImageThumbnails: true, CacheMaxBytes: cfg.ThumbnailCacheBytes, StreamMaxBytes: cfg.StreamMaxBytes, ServerDownloads: true, SegmentedDownloads: true}, nil
	}
	seeded := make(map[string]profiles.Profile)
	if !unclaimed {
		for index, seed := range []struct{ key, name, path, auth string }{
			{"library", "Synthetic library", "library", "none"},
			{"locked", "Locked source", "locked", "basic"},
			{"offline", "Offline source", "offline", "none"},
			{"unsupported", "Unsupported source", "unsupported", "none"},
		} {
			profile, err := profileService.Create(ctx, profiles.Input{Name: seed.name, RootURL: "http://fixture.example.test/" + seed.path + "/", AuthType: seed.auth, AllowHTTP: true, Position: index}, auth.Principal{Owner: true, CreatorID: "owner"})
			if err != nil {
				return err
			}
			seeded[seed.key] = profile
		}
	}
	control := http.NewServeMux()
	control.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": seeded, "origin": appOrigin, "supervisor_pid": os.Getppid()})
	})
	control.HandleFunc("POST /source", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Offline bool  `json:"offline"`
			DelayMS int64 `json:"delay_ms"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.DelayMS < 0 || input.DelayMS > 10000 {
			http.Error(w, "Invalid source state", 400)
			return
		}
		source.offline.Store(input.Offline)
		source.delay.Store(input.DelayMS)
		w.WriteHeader(204)
	})
	control.HandleFunc("POST /expire-sessions", func(w http.ResponseWriter, r *http.Request) {
		if _, err := db.DB.ExecContext(r.Context(), "UPDATE auth_sessions SET expires_at=0"); err != nil {
			http.Error(w, "Could not expire sessions", 500)
			return
		}
		w.WriteHeader(204)
	})
	control.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204); stop() })
	controlServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != controlAddress || r.Header.Get("Origin") != "" || r.Header.Get("X-Fixture-Control") != controlKey {
			http.Error(w, "Fixture control denied", 403)
			return
		}
		control.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	controlListener, err := net.Listen("tcp", controlAddress)
	if err != nil {
		return err
	}
	defer controlServer.Close()
	go controlServer.Serve(controlListener)
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: app.Handler(webui.Handler()), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	failures := make(chan error, 1)
	go func() { failures <- server.Serve(listener) }()
	fmt.Println("WEB_FIXTURE_READY " + appOrigin)
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Let the control handler flush its shutdown response before closing sockets.
	controlErr := controlServer.Shutdown(shutdown)
	return errors.Join(controlErr, server.Shutdown(shutdown))
}

func runtimeDirectory() (string, error) {
	if len(os.Args) == 1 {
		return os.MkdirTemp("", "folderhug-web-fixture-")
	}
	if (len(os.Args) != 3 && !(len(os.Args) == 4 && os.Args[3] == "--unclaimed")) || os.Args[1] != "--runtime-dir" || !filepath.IsAbs(os.Args[2]) {
		return "", fmt.Errorf("usage: web-fixture [--runtime-dir ABSOLUTE_NEW_DIRECTORY [--unclaimed]]")
	}
	dir := filepath.Clean(os.Args[2])
	// Mkdir must succeed so cleanup only removes a directory this process made.
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
