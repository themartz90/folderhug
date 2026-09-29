package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/preferences"
)

func (s *Server) preferenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/preferences", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		value, err := s.Preferences.Get(r.Context(), p)
		if err != nil {
			s.preferenceFailure(w, r, err)
			return
		}
		writeJSON(w, 200, value)
	}))
	mux.HandleFunc("PUT /api/v1/preferences", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input struct {
			Theme         *string `json:"theme"`
			Accent        *string `json:"accent"`
			Density       *string `json:"density"`
			FilterPresets []struct {
				ID        string                     `json:"id"`
				Name      string                     `json:"name"`
				ProfileID *string                    `json:"profile_id"`
				Filters   *preferences.PresetFilters `json:"filters"`
			} `json:"filter_presets"`
			ProfileOrder         []string                        `json:"profile_order"`
			LastDestination      *preferences.DestinationFolder  `json:"last_destination"`
			DestinationFavorites []preferences.DestinationFolder `json:"destination_favorites"`
			ProfileViews         *map[string]struct {
				View         string `json:"view"`
				Sort         string `json:"sort"`
				Direction    string `json:"direction"`
				FoldersFirst *bool  `json:"folders_first"`
			} `json:"profile_views"`
		}
		var raw json.RawMessage
		if !decode(w, r, &raw) {
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if !validPreferencesJSON(json.NewDecoder(bytes.NewReader(raw))) || decoder.Decode(&input) != nil {
			s.preferenceFailure(w, r, preferences.ErrInvalid)
			return
		}
		if input.Theme == nil || input.ProfileViews == nil {
			s.preferenceFailure(w, r, preferences.ErrInvalid)
			return
		}
		value := preferences.Preferences{Theme: *input.Theme, ProfileViews: map[string]preferences.ProfileView{}}
		if input.Accent != nil {
			if *input.Accent == "" {
				s.preferenceFailure(w, r, preferences.ErrInvalid)
				return
			}
			value.Accent = *input.Accent
		}
		if input.Density != nil {
			if *input.Density == "" {
				s.preferenceFailure(w, r, preferences.ErrInvalid)
				return
			}
			value.Density = *input.Density
		}
		if input.FilterPresets != nil {
			value.FilterPresets = []preferences.FilterPreset{}
			for _, preset := range input.FilterPresets {
				if preset.Filters == nil || preset.ProfileID != nil && *preset.ProfileID == "" {
					s.preferenceFailure(w, r, preferences.ErrInvalid)
					return
				}
				item := preferences.FilterPreset{ID: preset.ID, Name: preset.Name, Filters: *preset.Filters}
				if preset.ProfileID != nil {
					item.ProfileID = *preset.ProfileID
				}
				value.FilterPresets = append(value.FilterPresets, item)
			}
		}
		value.ProfileOrder = input.ProfileOrder
		value.LastDestination = input.LastDestination
		value.DestinationFavorites = input.DestinationFavorites
		for id, view := range *input.ProfileViews {
			if view.FoldersFirst == nil {
				s.preferenceFailure(w, r, preferences.ErrInvalid)
				return
			}
			value.ProfileViews[id] = preferences.ProfileView{View: view.View, Sort: view.Sort, Direction: view.Direction, FoldersFirst: *view.FoldersFirst}
		}
		value, err := s.Preferences.Put(r.Context(), p, value)
		if err != nil {
			s.preferenceFailure(w, r, err)
			return
		}
		writeJSON(w, 200, value)
	}))
	mux.HandleFunc("GET /api/v1/bookmarks", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		items, err := s.Preferences.ListBookmarks(r.Context(), p)
		if err != nil {
			s.preferenceFailure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"bookmarks": items})
	}))
	mux.HandleFunc("POST /api/v1/bookmarks", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input struct {
			ProfileID   string          `json:"profile_id"`
			DirectoryID string          `json:"directory_id"`
			Name        string          `json:"name"`
			Position    json.RawMessage `json:"position"`
		}
		if !decode(w, r, &input) {
			return
		}
		position := 0
		if len(input.Position) > 0 && (string(input.Position) == "null" || json.Unmarshal(input.Position, &position) != nil) {
			s.preferenceFailure(w, r, preferences.ErrInvalid)
			return
		}
		item, err := s.Preferences.CreateBookmark(r.Context(), p, preferences.BookmarkCreate{ProfileID: input.ProfileID, DirectoryID: input.DirectoryID, Name: input.Name, Position: position})
		if err != nil {
			s.preferenceFailure(w, r, err)
			return
		}
		writeJSON(w, 201, item)
	}))
	mux.HandleFunc("DELETE /api/v1/bookmarks/{id}", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := s.Preferences.DeleteBookmark(r.Context(), p, r.PathValue("id")); err != nil {
			s.preferenceFailure(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/status", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if s.StatusProvider == nil {
			problem(w, r, 503, "unavailable", "Application status is unavailable.")
			return
		}
		status, err := s.StatusProvider(r.Context())
		if err != nil || status.CacheMaxBytes < 0 || status.StreamMaxBytes < 0 || (status.VideoWorker != "disabled" && status.VideoWorker != "ready" && status.VideoWorker != "unavailable") {
			problem(w, r, 503, "unavailable", "Application status is unavailable.")
			return
		}
		writeJSON(w, 200, status)
	}))
}

// Optional fields may be omitted, but explicit nulls and duplicate keys are ambiguous.
func validPreferencesJSON(decoder *json.Decoder) bool {
	var value func() bool
	value = func() bool {
		token, err := decoder.Token()
		if err != nil || token == nil {
			return false
		}
		delim, nested := token.(json.Delim)
		if !nested {
			return true
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || keys[name] || !value() {
					return false
				}
				keys[name] = true
			}
		case '[':
			for decoder.More() {
				if !value() {
					return false
				}
			}
		default:
			return false
		}
		_, err = decoder.Token()
		return err == nil
	}
	if !value() {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func (s *Server) preferenceFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, preferences.ErrForbidden):
		problem(w, r, 403, "forbidden", "This action requires the owner session.")
	case errors.Is(err, preferences.ErrInvalid):
		problem(w, r, 400, "invalid_request", "Check the preference or bookmark settings.")
	case errors.Is(err, preferences.ErrNotFound):
		problem(w, r, 404, "not_found", "This profile or bookmark does not exist.")
	case errors.Is(err, preferences.ErrLimit):
		problem(w, r, 409, "invalid_state", "Remove a bookmark before adding another.")
	default:
		s.failure(w, r, err)
	}
}
