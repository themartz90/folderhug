package httpapi

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloads"
)

func (s *Server) downloadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/destinations", s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		items, err := s.Downloads.Destinations(p.Owner)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"destinations": items})
	}))
	mux.HandleFunc("GET /api/v1/destinations/{id}/folders", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		page, err := s.Downloads.Folders(p.Owner, r.PathValue("id"), r.URL.Query().Get("path"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	}))
	mux.HandleFunc("POST /api/v1/downloads", s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input downloads.Input
		if !decode(w, r, &input) {
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			s.failure(w, r, downloads.ErrInvalid)
			return
		}
		item, err := s.Downloads.Create(r.Context(), p, input, r.Header.Get("Idempotency-Key"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 202, item)
	}))
	mux.HandleFunc("GET /api/v1/downloads", s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		limit, cursor, err := pageQuery(r.URL.Query(), "state")
		if err != nil {
			s.failure(w, r, err)
			return
		}
		page, err := s.Downloads.List(r.Context(), p, downloads.Query{Limit: limit, Cursor: cursor, State: r.URL.Query().Get("state")})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	}))
	mux.HandleFunc("GET /api/v1/downloads/{id}", s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		item, err := s.Downloads.Get(r.Context(), p, r.PathValue("id"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, item)
	}))
	for _, action := range []string{"pause", "resume", "cancel", "retry"} {
		mux.HandleFunc("POST /api/v1/downloads/{id}/"+action, s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
			var input struct {
				AllowRestart bool `json:"allow_restart"`
			}
			if action == "resume" && r.Body != nil && r.ContentLength != 0 {
				if !decode(w, r, &input) {
					return
				}
			}
			item, err := s.Downloads.Action(r.Context(), p, r.PathValue("id"), action, input.AllowRestart)
			if err != nil {
				s.failure(w, r, err)
				return
			}
			writeJSON(w, 202, item)
		}))
	}
	mux.HandleFunc("DELETE /api/v1/downloads/{id}/partial", s.authorized(false, "downloads:write", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := s.Downloads.DeletePartial(r.Context(), p, r.PathValue("id")); err != nil {
			s.failure(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("DELETE /api/v1/downloads/{id}/content", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := s.Downloads.DeleteCompleted(r.Context(), p, r.PathValue("id")); err != nil {
			s.failure(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/downloads/{id}/content", s.authorized(false, "content:read", s.downloadContent))
}

func (s *Server) downloadContent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	f, item, err := s.Downloads.Completed(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.failure(w, r, err)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if len(r.Header.Values("Range")) > 1 || strings.Contains(r.Header.Get("Range"), ",") {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(stat.Size(), 10))
		problem(w, r, 416, "invalid_range", "Request one byte range.")
		return
	}
	timeout := s.Config.StreamTimeout
	if timeout <= 0 {
		timeout = 24 * time.Hour
	}
	ctx, stop := context.WithTimeout(r.Context(), timeout)
	defer stop()
	ctx, cancel := s.completedDownloadContext(ctx, p, item.ID)
	defer cancel()
	writer, finish, err := content.ResponseWriter(ctx, w)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	defer finish()
	name := item.Name
	if item.SavedName != nil {
		name = *item.SavedName
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+item.ID+`"`)
	output := &downloadWriter{ResponseWriter: writer}
	http.ServeContent(output, r.WithContext(ctx), name, stat.ModTime(), &downloadReader{ReadSeeker: f, ctx: ctx})
	if r.Method != "HEAD" && (output.status == 200 || output.status == 206) {
		expected, err := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
		if err != nil || output.written != expected || ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func (s *Server) completedDownloadContext(parent context.Context, p auth.Principal, id string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A valid token can lose this file when its profile is deleted
				// or its scope or the job's creator changes.
				if err := s.Downloads.CheckCompletedAccess(ctx, p, id); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

type downloadReader struct {
	io.ReadSeeker
	ctx context.Context
}

func (r *downloadReader) Read(b []byte) (int, error) {
	if r.ctx.Err() != nil {
		panic(http.ErrAbortHandler)
	}
	n, err := r.ReadSeeker.Read(b)
	if err != nil && err != io.EOF {
		panic(http.ErrAbortHandler)
	}
	return n, err
}

type downloadWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *downloadWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *downloadWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	if err != nil || n != len(b) {
		panic(http.ErrAbortHandler)
	}
	return n, nil
}
