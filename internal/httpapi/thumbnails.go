package httpapi

import (
	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/content"
	"io"
	"net/http"
	"strconv"
)

func (s *Server) thumbnailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/profiles/{id}/thumbnails", s.authorized(false, "content:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input struct {
			EntryIDs []*string `json:"entry_ids"`
		}
		if !decode(w, r, &input) {
			return
		}
		ids := make([]string, len(input.EntryIDs))
		for i, id := range input.EntryIDs {
			if id == nil || *id == "" {
				problem(w, r, 400, "invalid_request", "Each entry ID must be a nonempty string.")
				return
			}
			ids[i] = *id
		}
		result, err := s.Thumbnails.Request(r.Context(), p, r.PathValue("id"), ids)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 202, map[string]any{"thumbnails": result})
	}))
	mux.HandleFunc("GET /api/v1/thumbnails/{id}", s.authorized(false, "content:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		item, file, err := s.Thumbnails.Get(r.Context(), p, r.PathValue("id"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if file == nil {
			if item.State == "pending" {
				w.Header().Set("Retry-After", "2")
				writeJSON(w, 202, item)
				return
			}
			problem(w, r, 404, "unsupported_media", "A thumbnail is not available for this file.")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			s.failure(w, r, err)
			return
		}
		ctx, cancel := s.liveContext(r.Context(), p)
		defer cancel()
		writer, finish, err := content.ResponseWriter(ctx, w)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		defer finish()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.WriteHeader(200)
		if r.Method == "HEAD" {
			return
		}
		if _, err = io.Copy(writer, file); err != nil {
			panic(http.ErrAbortHandler)
		}
	}))
}
