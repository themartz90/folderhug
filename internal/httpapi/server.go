package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/config"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/preferences"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/scans"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/updates"
	"github.com/themartz90/folderhug/internal/upstream"
)

type Server struct {
	BuildInfo      buildinfo.Info
	Updates        *updates.Service
	Config         config.Config
	Store          *store.Store
	Auth           *auth.Service
	Profiles       *profiles.Service
	Catalog        *catalog.Service
	Content        *content.Service
	Events         *events.Hub
	Scans          *scans.Service
	Downloads      *downloads.Service
	Thumbnails     *thumbnails.Service
	Preferences    *preferences.Service
	StatusProvider preferences.StatusProvider
	Upstream       interface {
		Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)
	}
}

func (s *Server) Handler(ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	if s.Auth != nil {
		s.authRoutes(mux)
		s.releaseRoutes(mux)
		if s.Catalog != nil && s.Content != nil {
			s.browseRoutes(mux)
		}
		if s.Scans != nil {
			s.scanRoutes(mux)
		}
		if s.Downloads != nil {
			s.downloadRoutes(mux)
		}
		if s.Thumbnails != nil {
			s.thumbnailRoutes(mux)
		}
		if s.Preferences != nil {
			s.preferenceRoutes(mux)
		}
		if s.Events != nil {
			mux.HandleFunc("GET /api/v1/events", s.authorized(false, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
				if err := s.Events.Serve(w, r, p); err != nil {
					s.failure(w, r, err)
				}
			}))
		}
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.Store.DB.PingContext(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, r, 404, "not_found", "This endpoint does not exist.")
	})
	mux.Handle("/", ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := make([]byte, 12)
		rand.Read(id)
		w.Header().Set("X-Request-ID", hex.EncodeToString(id))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.URL.Path != "/health/live" && r.URL.Path != "/health/ready" {
			if _, allowed := s.requestOrigin(r); !allowed {
				problem(w, r, 421, "unsafe_origin", "Open Folderhug using the server IP, or configure its public URL for this hostname.")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message, "request_id": w.Header().Get("X-Request-ID")})
}
