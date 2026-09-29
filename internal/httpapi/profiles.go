package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/upstream"
)

func (s *Server) profileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/profiles/{id}/test", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		profile, credentials, ctx, cancel, err := s.Profiles.Access(r.Context(), r.PathValue("id"), profiles.LeaseID(p))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		defer cancel()
		root, err := upstream.NewRoot(profile.RootURL, profile.AllowHTTP)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		response, err := s.Upstream.Do(ctx, root, http.MethodGet, "", upstream.Options{Credentials: credentials, Interactive: true})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		defer response.Body.Close()
		if response.StatusCode == 401 || response.StatusCode == 403 {
			problem(w, r, 502, "upstream_auth_required", "The source did not accept these credentials.")
			return
		}
		if response.StatusCode != 200 {
			problem(w, r, 502, "upstream_unavailable", "The source did not return a directory listing.")
			return
		}
		current := root.URL()
		if response.Request != nil {
			current = response.Request.URL
		}
		if relative, err := root.Relative(current); err != nil || relative != "" {
			problem(w, r, 502, "unsupported_listing", "The source redirects to a different directory. Use that directory as the profile root.")
			return
		}
		_, format, err := listing.Parse(response.Body, root, current, profile.ID, profile.Generation)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready", "listing_format": format, "canonical_root_url": current.String(), "range_observation": "not_tested"})
	}))
	mux.HandleFunc("GET /api/v1/profiles", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		items, err := s.Profiles.List(r.Context(), p)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"profiles": items})
	}))
	mux.HandleFunc("POST /api/v1/profiles", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var in profiles.Input
		if !decode(w, r, &in) {
			return
		}
		var item profiles.Profile
		err := s.Auth.WithLivePrincipal(r.Context(), p, func(current auth.Principal) error {
			var err error
			item, err = s.Profiles.Create(r.Context(), in, current)
			return err
		})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 201, item)
	}))
	mux.HandleFunc("GET /api/v1/profiles/{id}", s.authorized(false, "browse:read", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "browse:read") {
			return
		}
		item, err := s.Profiles.Get(r.Context(), id, profiles.LeaseID(p))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, item)
	}))
	mux.HandleFunc("PATCH /api/v1/profiles/{id}", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var in map[string]json.RawMessage
		if !decode(w, r, &in) {
			return
		}
		var item profiles.Profile
		err := s.Auth.WithLivePrincipal(r.Context(), p, func(current auth.Principal) error {
			var err error
			item, err = s.Profiles.Patch(r.Context(), r.PathValue("id"), in, current)
			return err
		})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, item)
	}))
	mux.HandleFunc("DELETE /api/v1/profiles/{id}", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := s.Profiles.Delete(r.Context(), r.PathValue("id")); err != nil {
			s.failure(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/v1/profiles/{id}/unlock", s.authorized(false, "credentials:unlock", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "credentials:unlock") {
			return
		}
		profile, err := s.Profiles.Get(r.Context(), id, profiles.LeaseID(p))
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if profile.AuthType != "basic" {
			problem(w, r, 409, "invalid_state", "This profile does not use a password.")
			return
		}
		var in struct {
			Username     *string `json:"username"`
			Password     *string `json:"password"`
			LeaseSeconds *int    `json:"lease_seconds"`
		}
		if !decode(w, r, &in) {
			return
		}
		leaseSeconds := 3600
		if in.LeaseSeconds != nil {
			leaseSeconds = *in.LeaseSeconds
		}
		if in.Username == nil || in.Password == nil || leaseSeconds < 60 || leaseSeconds > 28800 || len(*in.Username) > 1024 || len(*in.Password) > 4096 || strings.ContainsAny(*in.Username, ":\r\n") || strings.ContainsAny(*in.Password, "\r\n") {
			problem(w, r, 400, "invalid_request", "Check the credential lease settings.")
			return
		}
		expires := time.Now().Add(time.Duration(leaseSeconds) * time.Second)
		if !p.ExpiresAt.IsZero() && p.ExpiresAt.Before(expires) {
			expires = p.ExpiresAt
		}
		err = s.Auth.WithLivePrincipal(r.Context(), p, func(current auth.Principal) error {
			if !current.Can(id, "credentials:unlock") {
				return profiles.ErrNotFound
			}
			return s.Profiles.Unlock(profile, profiles.LeaseID(current), profiles.Credentials{Username: *in.Username, Password: *in.Password}, expires)
		})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"expires_at": expires})
	}))
	mux.HandleFunc("POST /api/v1/profiles/{id}/lock", s.authorized(false, "credentials:unlock", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if !s.profileAllowed(w, r, p, id, "credentials:unlock") {
			return
		}
		if _, err := s.Profiles.Get(r.Context(), id, profiles.LeaseID(p)); err != nil {
			s.failure(w, r, err)
			return
		}
		s.Profiles.Lock(id, profiles.LeaseID(p))
		w.WriteHeader(204)
	}))
}

func (s *Server) profileAllowed(w http.ResponseWriter, r *http.Request, p auth.Principal, id, scope string) bool {
	if !p.Can(id, scope) {
		problem(w, r, 404, "not_found", "This item does not exist.")
		return false
	}
	return true
}
