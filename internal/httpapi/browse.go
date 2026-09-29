package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

func (s *Server) browseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/profiles/{id}/browse", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "browse:read") {
			return
		}
		query, err := browseQuery(r.URL.Query())
		if err != nil {
			s.failure(w, r, err)
			return
		}
		page, err := s.Catalog.Browse(r.Context(), id, profiles.LeaseID(p), r.URL.Query().Get("directory_id"), query)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	}))
	mux.HandleFunc("GET /api/v1/profiles/{id}/entries/{entry_id}", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "browse:read") {
			return
		}
		entry, err := s.Catalog.Entry(r.Context(), id, profiles.LeaseID(p), r.PathValue("entry_id"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if entry.Kind == "file" {
			profile, credentials, ctx, cancel, err := s.Profiles.Access(r.Context(), id, profiles.LeaseID(p))
			if err != nil {
				s.failure(w, r, err)
				return
			}
			defer cancel()
			if listing.ID(id, profile.Generation, entry.EscapedPath) != entry.ID {
				s.failure(w, r, catalog.ErrNotFound)
				return
			}
			root, err := upstream.NewRoot(profile.RootURL, profile.AllowHTTP)
			if err != nil {
				s.failure(w, r, err)
				return
			}
			meta, err := s.Content.Metadata(ctx, content.Source{Root: root, Path: entry.EscapedPath, Filename: entry.Name, Credentials: credentials})
			if err != nil {
				s.failure(w, r, err)
				return
			}
			if meta.SizeExact {
				entry.Size = meta.Size
				entry.SizeExact = true
			}
			if meta.ModifiedAt != nil {
				entry.ModifiedAt = meta.ModifiedAt
				entry.TimezoneKnown = true
			}
			entry.Provenance = "response_headers"
		}
		writeJSON(w, 200, entry)
	}))
	mux.HandleFunc("GET /api/v1/profiles/{id}/entries/{entry_id}/content", s.authorized(false, "content:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "content:read") {
			return
		}
		query := r.URL.Query()
		disposition := query.Get("disposition")
		if len(query) > 1 || len(query["disposition"]) > 1 || (len(query) == 1 && !query.Has("disposition")) || (disposition != "" && disposition != "attachment" && disposition != "preview") {
			problem(w, r, 400, "invalid_request", "Choose attachment or preview.")
			return
		}
		entry, err := s.Catalog.Entry(r.Context(), id, profiles.LeaseID(p), r.PathValue("entry_id"))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if entry.Kind != "file" {
			problem(w, r, 400, "invalid_request", "Select a file to download.")
			return
		}
		profile, credentials, ctx, cancel, err := s.Profiles.Access(r.Context(), id, profiles.LeaseID(p))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		defer cancel()
		if listing.ID(id, profile.Generation, entry.EscapedPath) != entry.ID {
			s.failure(w, r, catalog.ErrNotFound)
			return
		}
		if disposition == "preview" && !profile.Previews {
			problem(w, r, 409, "invalid_state", "Previews are disabled for this profile.")
			return
		}
		root, err := upstream.NewRoot(profile.RootURL, profile.AllowHTTP)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		live, stop := s.liveContext(ctx, p)
		defer stop()
		if err = s.Content.Serve(w, r.WithContext(live), content.Source{Root: root, Path: entry.EscapedPath, Filename: entry.Name, Credentials: credentials}, disposition == "preview"); err != nil {
			s.failure(w, r, err)
		}
	}))
}

func browseQuery(values url.Values) (catalog.Query, error) {
	q := catalog.Query{}
	for key, items := range values {
		if len(items) != 1 {
			return q, catalog.ErrInvalid
		}
		v := items[0]
		switch key {
		case "directory_id":
			if len(v) > 128 {
				return q, catalog.ErrInvalid
			}
		case "cursor":
			q.Cursor = v
		case "name":
			q.Name = v
		case "category":
			q.Category = v
		case "extensions":
			q.Extensions = v
		case "sort":
			q.Sort = v
		case "direction":
			q.Direction = v
		case "limit":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1000 {
				return q, catalog.ErrInvalid
			}
			q.Limit = n
		case "min_size", "max_size":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return q, catalog.ErrInvalid
			}
			if key == "min_size" {
				q.MinSize = &n
			} else {
				q.MaxSize = &n
			}
		case "modified_after", "modified_before":
			date, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return q, catalog.ErrInvalid
			}
			if key == "modified_after" {
				q.ModifiedAfter = &date
			} else {
				q.ModifiedBefore = &date
			}
		case "show_folders", "folders_first", "refresh":
			if v != "true" && v != "false" {
				return q, catalog.ErrInvalid
			}
			flag := v == "true"
			switch key {
			case "show_folders":
				q.ShowFolders = &flag
			case "folders_first":
				q.FoldersFirst = &flag
			case "refresh":
				q.Refresh = flag
			}
		default:
			return q, catalog.ErrInvalid
		}
	}
	return q, nil
}
