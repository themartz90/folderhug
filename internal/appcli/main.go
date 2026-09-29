package appcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/httpapi"
	"github.com/themartz90/folderhug/internal/mediabroker"
	"github.com/themartz90/folderhug/internal/preferences"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/scans"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/upstream"
	"github.com/themartz90/folderhug/internal/webui"
)

func Main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/health/ready")
		if err != nil {
			return fmt.Errorf("readiness check failed")
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("app is not ready")
		}
		return nil
	}
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command != "serve" && command != "dev" && command != "setup" && command != "recover" && command != "setup-media" {
		return fmt.Errorf("usage: folderhug [serve|dev|setup|recover|setup-media|healthcheck]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if command == "setup-media" {
		if len(os.Args) != 2 {
			return fmt.Errorf("setup-media does not accept arguments")
		}
		return setupMedia(cfg.WorkerKeyFile)
	}
	if command == "dev" {
		if len(os.Args) != 2 {
			return fmt.Errorf("dev does not accept arguments")
		}
		if err = configureDev(&cfg); err != nil {
			return err
		}
	}
	if err = checkMissingDatabase(cfg); err != nil {
		return err
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	owner := auth.New(db.DB)
	if command == "setup" || command == "recover" {
		return configureOwner(context.Background(), command, os.Args[2:], cfg, db, owner)
	}
	if len(os.Args) > 2 {
		return fmt.Errorf("serve does not accept arguments")
	}
	vault, err := prepareVault(context.Background(), cfg, db)
	if err != nil {
		return err
	}
	var devCookie string
	if command == "dev" {
		devCookie, err = devSession(context.Background(), cfg, db, owner)
		if err != nil {
			return err
		}
		defer owner.Logout(context.Background(), devCookie)
		slog.Info("Local development mode: automatic sign-in, separate data in .local/dev")
	}
	profileService := profiles.New(db.DB, vault)
	defer profileService.Close()
	transport := upstream.New(cfg.UpstreamPolicy)
	defer transport.CloseIdleConnections()
	app := &httpapi.Server{Config: cfg, Store: db, Auth: owner, Profiles: profileService, Upstream: transport}
	app.Catalog = catalog.New(db.DB, profileService, transport)
	app.Content = content.New(transport, content.Limits{MaxBytes: cfg.StreamMaxBytes, Timeout: cfg.StreamTimeout})
	app.Events = events.New(owner)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Scans = scans.New(db.DB, profileService, owner, app.Catalog, transport, app.Events)
	if err = app.Scans.Start(ctx); err != nil {
		return err
	}
	defer app.Scans.Close()
	if err = os.MkdirAll(cfg.DownloadsDir, 0700); err != nil {
		return fmt.Errorf("create downloads directory")
	}
	downloadFiles, err := downloadfs.Open(cfg.DownloadsDir)
	if err != nil {
		return err
	}
	defer downloadFiles.Close()
	reserve := cfg.DownloadReserveBytes
	if reserve == 0 {
		reserve = -1
	}
	app.Downloads = downloads.New(db.DB, profileService, owner, app.Catalog, transport, app.Events, downloadFiles, downloads.Limits{StorageBudgetBytes: cfg.DownloadBudgetBytes, FreeReserveBytes: reserve, UnknownSizeMaxBytes: cfg.DownloadUnknownBytes, RequestTimeout: cfg.StreamTimeout})
	mounts, err := prepareDownloadMounts(cfg)
	if err != nil {
		return err
	}
	app.Downloads.ConfigureMounts(mounts)
	if err = app.Downloads.Start(ctx); err != nil {
		return err
	}
	defer app.Downloads.Close()
	var broker *mediabroker.Broker
	thumbnailConfig := thumbnails.Config{CacheDir: cfg.CacheDir, MaxBytes: cfg.ThumbnailCacheBytes}
	if cfg.MediaSocket != "" {
		key, e := readWorkerKey(cfg.WorkerKeyFile)
		if e != nil {
			return e
		}
		broker, e = mediabroker.New(profileService, owner, app.Catalog, transport, key)
		if e != nil {
			return e
		}
		if e = broker.Start(cfg.MediaSocket); e != nil {
			return e
		}
		defer broker.Close()
		thumbnailConfig.Video = broker
	}
	app.Thumbnails, err = thumbnails.New(db.DB, profileService, owner, app.Catalog, transport, app.Events, thumbnailConfig)
	if err != nil {
		return err
	}
	defer app.Thumbnails.Close()
	app.Preferences = preferences.New(db.DB, profileService, app.Catalog)
	app.StatusProvider = func(context.Context) (preferences.Status, error) {
		video := "disabled"
		if broker != nil {
			video = "unavailable"
			if broker.Ready() {
				video = "ready"
			}
		}
		return preferences.Status{ImageThumbnails: true, VideoWorker: video, CacheMaxBytes: cfg.ThumbnailCacheBytes, StreamMaxBytes: cfg.StreamMaxBytes, ServerDownloads: true, SegmentedDownloads: true}, nil
	}
	var handler http.Handler = app.Handler(webui.Handler())
	if command == "dev" {
		handler = devHandler(handler, devCookie, cfg.PublicURL.String())
	}
	srv := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("Folderhug started", "address", cfg.Address)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
