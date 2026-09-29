package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/scans"
)

func (s *Server) scanRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/profiles/{id}/scans", s.authorized(false, "scan:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input scans.Input
		if !decode(w, r, &input) {
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			s.failure(w, r, scans.ErrInvalid)
			return
		}
		item, err := s.Scans.Create(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 202, item)
	}))
	mux.HandleFunc("GET /api/v1/scans", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		limit, cursor, err := pageQuery(r.URL.Query(), "profile_id")
		if err != nil {
			s.failure(w, r, err)
			return
		}
		items, err := s.Scans.List(r.Context(), p, r.URL.Query().Get("profile_id"), limit, cursor)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, items)
	}))
	mux.HandleFunc("GET /api/v1/scans/{id}", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		item, err := s.Scans.Get(r.Context(), p, r.PathValue("id"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, item)
	}))
	for _, action := range []string{"pause", "resume", "cancel", "retry"} {
		mux.HandleFunc("POST /api/v1/scans/{id}/"+action, s.authorized(false, "scan:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
			item, err := s.Scans.Action(r.Context(), p, r.PathValue("id"), action)
			if err != nil {
				s.failure(w, r, err)
				return
			}
			writeJSON(w, 202, item)
		}))
	}
	mux.HandleFunc("GET /api/v1/search", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		values := r.URL.Query()
		q := scans.SearchQuery{Q: values.Get("q"), Match: values.Get("match"), ProfileID: values.Get("profile_id"), DirectoryID: values.Get("directory_id")}
		for _, key := range []string{"q", "match", "profile_id", "directory_id"} {
			if len(values[key]) > 1 {
				s.failure(w, r, scans.ErrInvalid)
				return
			}
			values.Del(key)
		}
		if values.Has("direct_only") {
			if len(values["direct_only"]) != 1 || values.Get("direct_only") != "true" {
				s.failure(w, r, scans.ErrInvalid)
				return
			}
			q.DirectOnly = true
			values.Del("direct_only")
		}
		if values.Has("categories") {
			if len(values["categories"]) != 1 {
				s.failure(w, r, scans.ErrInvalid)
				return
			}
			q.Categories = strings.Split(values.Get("categories"), ",")
			values.Del("categories")
		}
		for key := range values {
			switch key {
			case "limit", "cursor", "category", "extensions", "min_size", "max_size", "modified_after", "modified_before":
			default:
				s.failure(w, r, scans.ErrInvalid)
				return
			}
		}
		var err error
		q.Query, err = browseQuery(values)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		page, err := s.Scans.Search(r.Context(), p, q)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	}))
	mux.HandleFunc("GET /api/v1/scans/{id}/export", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		values := r.URL.Query()
		format := values.Get("format")
		if format == "" {
			format = "json"
		}
		if len(values) > 1 || (len(values) == 1 && !values.Has("format")) || len(values["format"]) > 1 || (format != "json" && format != "csv") {
			s.failure(w, r, scans.ErrInvalid)
			return
		}
		id := r.PathValue("id")
		if _, err := s.Scans.Get(r.Context(), p, id); err != nil {
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
		w.Header().Set("Content-Type", "application/json")
		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		}
		w.Header().Set("Content-Disposition", "attachment; filename=scan."+format)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "no-store")
		output := &exportWriter{ResponseWriter: writer}
		if err = s.Scans.Export(ctx, p, id, format, output); err != nil {
			if output.written {
				panic(http.ErrAbortHandler)
			}
			w.Header().Del("Content-Disposition")
			s.failure(w, r, err)
		}
	}))
}

type exportWriter struct {
	http.ResponseWriter
	written bool
}

func (w *exportWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

func pageQuery(values url.Values, extra string) (limit int, cursor string, err error) {
	limit = 200
	for key, items := range values {
		if len(items) != 1 {
			return 0, "", scans.ErrInvalid
		}
		switch key {
		case "limit":
			limit, err = strconv.Atoi(items[0])
			if err != nil || limit < 1 || limit > 1000 {
				return 0, "", scans.ErrInvalid
			}
		case "cursor":
			cursor = items[0]
			if len(cursor) > 2048 {
				return 0, "", scans.ErrInvalid
			}
		default:
			if key != extra || len(items[0]) > 128 {
				return 0, "", scans.ErrInvalid
			}
		}
	}
	return limit, cursor, nil
}
