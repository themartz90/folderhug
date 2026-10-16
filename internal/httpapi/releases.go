package httpapi

import (
	"net/http"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/releaseinfo"
	"github.com/themartz90/folderhug/internal/updates"
)

func (s *Server) releaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/release", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
		current := s.BuildInfo
		if current == (buildinfo.Info{}) {
			current = buildinfo.Current()
		}
		writeJSON(w, http.StatusOK, struct {
			Current    buildinfo.Info          `json:"current"`
			Highlights *releaseinfo.Highlights `json:"highlights"`
		}{current, releaseinfo.HighlightsFor(current.Version)})
	}))
	mux.HandleFunc("GET /api/v1/updates", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
		status := updates.Status{State: "unknown"}
		if s.Updates != nil {
			status = s.Updates.Status(r.Context())
		}
		writeJSON(w, http.StatusOK, status)
	}))
}
