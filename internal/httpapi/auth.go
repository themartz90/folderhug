package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/downloads"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/scans"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/upstream"
)

const cookieName = "http_wrapper_session"

type loginAttempt struct {
	count int
	until time.Time
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		problem(w, r, 415, "invalid_request", "Send an application/json request.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		problem(w, r, 400, "invalid_request", "The request body is invalid.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, r, 400, "invalid_request", "Send one JSON object.")
		return false
	}
	return true
}

func (s *Server) authorized(owner bool, scope string, next func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie := ""
		if c, err := r.Cookie(cookieName); err == nil {
			cookie = c.Value
		}
		bearer := ""
		if value, present := r.Header["Authorization"]; present {
			if len(value) != 1 || !strings.HasPrefix(value[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(value[0], "Bearer ")) == "" {
				problem(w, r, 401, "unauthenticated", "Sign in or supply a valid API token.")
				return
			}
			bearer = strings.TrimPrefix(value[0], "Bearer ")
		}
		p, err := s.Auth.Authenticate(r.Context(), cookie, bearer)
		if err != nil {
			problem(w, r, 401, "unauthenticated", "Sign in or supply a valid API token.")
			return
		}
		if owner && !p.Owner {
			problem(w, r, 403, "forbidden", "This action requires the owner session.")
			return
		}
		if !p.Owner && scope != "" {
			allowed := false
			for _, v := range p.Scopes {
				if v == scope {
					allowed = true
				}
			}
			if !allowed {
				problem(w, r, 403, "forbidden", "This token does not allow this action.")
				return
			}
		}
		if p.Owner && r.Method != "GET" && r.Method != "HEAD" {
			if !s.validOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRFToken)) != 1 {
				problem(w, r, 403, "csrf_failed", "Refresh the page before retrying this action.")
				return
			}
		}
		next(w, r, p)
	}
}

func (s *Server) validOrigin(r *http.Request) bool {
	return s.Config.PublicURL != nil && r.Header.Get("Origin") == s.Config.PublicURL.String()
}

// Long responses stop when their session or token expires or is revoked.
func (s *Server) liveContext(parent context.Context, p auth.Principal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.Auth.Refresh(ctx, p); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func (s *Server) authRoutes(mux *http.ServeMux) {
	var rateMu sync.Mutex
	attempts := make(map[string]loginAttempt)
	hashSlots := make(chan struct{}, 2)
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !s.validOrigin(r) {
			problem(w, r, 403, "csrf_failed", "Sign in from this server's configured address.")
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		now := time.Now()
		rateMu.Lock()
		for key, a := range attempts {
			if now.After(a.until) {
				delete(attempts, key)
			}
		}
		a := attempts[ip]
		if a.until.IsZero() {
			a.until = now.Add(time.Minute)
		}
		limited := a.count >= 5 || len(attempts) >= 1024
		a.count++
		if !limited {
			attempts[ip] = a
		}
		rateMu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "60")
			problem(w, r, 429, "rate_limited", "Too many sign-in attempts. Try again in a minute.")
			return
		}
		select {
		case hashSlots <- struct{}{}:
			defer func() { <-hashSlots }()
		default:
			problem(w, r, 429, "rate_limited", "Sign-in is busy. Try again shortly.")
			return
		}
		var input struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		if len(input.Password) > 1024 {
			problem(w, r, 400, "invalid_request", "The password is too long.")
			return
		}
		raw, session, err := s.Auth.Login(r.Context(), input.Password)
		if err != nil {
			problem(w, r, 401, "unauthenticated", "The password was not accepted.")
			return
		}
		rateMu.Lock()
		delete(attempts, ip)
		rateMu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: raw, Path: "/", HttpOnly: true, Secure: s.Config.PublicURL.Scheme == "https", SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt})
		writeJSON(w, 200, session)
	})
	mux.HandleFunc("GET /api/v1/auth/session", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) { writeJSON(w, 200, p.Session()) }))
	mux.HandleFunc("POST /api/v1/auth/logout", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := s.Auth.RevokeSession(r.Context(), p.SessionID); err != nil {
			s.failure(w, r, err)
			return
		}
		s.Profiles.ClearPrincipal(profiles.LeaseID(p))
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.Config.PublicURL.Scheme == "https", SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(204)
	}))
	s.profileRoutes(mux)
	mux.HandleFunc("GET /api/v1/tokens", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		tokens, err := s.Auth.ListTokens(r.Context())
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"tokens": tokens})
	}))
	mux.HandleFunc("POST /api/v1/tokens", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var input auth.TokenCreate
		if !decode(w, r, &input) {
			return
		}
		for _, id := range input.ProfileIDs {
			if _, err := s.Profiles.Get(r.Context(), id, profiles.LeaseID(p)); err != nil {
				s.failure(w, r, err)
				return
			}
		}
		token, raw, err := s.Auth.CreateToken(r.Context(), input)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, 201, map[string]any{"token": token, "secret": raw})
	}))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.authorized(true, "", func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		if err := s.Auth.DeleteToken(r.Context(), id); err != nil {
			s.failure(w, r, err)
			return
		}
		s.Profiles.ClearPrincipal("token:" + id)
		w.WriteHeader(204)
	}))
}

func (s *Server) failure(w http.ResponseWriter, r *http.Request, err error) {
	var contentError *content.Error
	if errors.As(err, &contentError) {
		for key, values := range contentError.Headers {
			w.Header()[key] = values
		}
		problem(w, r, contentError.Status, contentError.Code, contentError.Message)
		return
	}
	switch {
	case errors.Is(err, thumbnails.ErrInvalid), errors.Is(err, downloads.ErrInvalid):
		problem(w, r, 400, "invalid_request", "The request is invalid.")
	case errors.Is(err, thumbnails.ErrNotFound), errors.Is(err, downloads.ErrNotFound):
		problem(w, r, 404, "not_found", "This item is not available.")
	case errors.Is(err, thumbnails.ErrBusy), errors.Is(err, downloads.ErrBusy):
		w.Header().Set("Retry-After", "5")
		problem(w, r, 429, "budget_exceeded", "The work queue is full. Try again later.")
	case errors.Is(err, thumbnails.ErrClosed):
		problem(w, r, 503, "unavailable", "Thumbnail processing is unavailable.")
	case errors.Is(err, downloads.ErrState):
		problem(w, r, 409, "invalid_state", "This action is not available for the download's current state.")
	case errors.Is(err, downloads.ErrIdempotency):
		problem(w, r, 409, "idempotency_conflict", "This request key was already used for a different download.")
	case errors.Is(err, downloads.ErrRestart):
		problem(w, r, 409, "restart_required", "The download must restart from zero.")
	case errors.Is(err, downloads.ErrBudget):
		problem(w, r, 507, "storage_full", "The download storage limit or free-space reserve was reached.")
	case errors.Is(err, downloads.ErrDestinationUnavailable):
		problem(w, r, 503, "unavailable", "The selected download folder is unavailable.")
	case errors.Is(err, scans.ErrInvalid):
		problem(w, r, 400, "invalid_request", "Check the scan or search settings.")
	case errors.Is(err, scans.ErrNotFound):
		problem(w, r, 404, "not_found", "This scan or profile does not exist.")
	case errors.Is(err, scans.ErrSuperseded):
		problem(w, r, 409, "scan_superseded", "A newer scan overlaps this folder. Start a new scan.")
	case errors.Is(err, scans.ErrInvalidState):
		problem(w, r, 409, "invalid_state", "This scan cannot perform that action in its current state.")
	case errors.Is(err, scans.ErrIdempotencyConflict):
		problem(w, r, 409, "idempotency_conflict", "This idempotency key was already used for another request.")
	case errors.Is(err, scans.ErrSnapshotExpired):
		problem(w, r, 409, "snapshot_expired", "The index changed. Search again from the first page.")
	case errors.Is(err, scans.ErrBudget):
		w.Header().Set("Retry-After", "5")
		problem(w, r, 429, "budget_exceeded", "The scan or search limit was reached.")
	case errors.Is(err, auth.ErrUnauthenticated):
		problem(w, r, 401, "unauthenticated", "Sign in or supply a valid API token.")
	case errors.Is(err, events.ErrForbidden):
		problem(w, r, 403, "forbidden", "This token does not allow event updates.")
	case errors.Is(err, events.ErrBusy):
		w.Header().Set("Retry-After", "5")
		problem(w, r, 429, "rate_limited", "Too many event streams are open.")
	case errors.Is(err, events.ErrStreamingUnsupported):
		problem(w, r, 503, "unavailable", "Event streaming is unavailable. Refresh the job status instead.")
	case errors.Is(err, catalog.ErrInvalid):
		problem(w, r, 400, "invalid_request", "Check the folder filters and sort settings.")
	case errors.Is(err, catalog.ErrNotFound):
		problem(w, r, 404, "not_found", "This entry does not exist in the current profile.")
	case errors.Is(err, catalog.ErrSnapshotExpired):
		problem(w, r, 409, "snapshot_expired", "This listing expired. Refresh the folder.")
	case errors.Is(err, catalog.ErrSourceAuth):
		problem(w, r, 502, "upstream_auth_required", "The source did not accept these credentials.")
	case errors.Is(err, catalog.ErrSourceUnavailable):
		problem(w, r, 502, "upstream_unavailable", "The source did not return a directory listing.")
	case errors.Is(err, catalog.ErrBudget):
		w.Header().Set("Retry-After", "5")
		problem(w, r, 429, "budget_exceeded", "The listing service is busy. Try again shortly.")
	case errors.Is(err, listing.ErrUnsupported):
		problem(w, r, 502, "unsupported_listing", "This source does not use a supported directory listing.")
	case errors.Is(err, listing.ErrLimit):
		problem(w, r, 413, "listing_limit", "This directory exceeds the listing limit.")
	case errors.Is(err, upstream.ErrPolicy), errors.Is(err, upstream.ErrURL):
		problem(w, r, 422, "unsafe_origin", "The source address is outside the configured connection policy.")
	case errors.Is(err, upstream.ErrResponseLimit):
		problem(w, r, 413, "budget_exceeded", "The source response exceeds the byte limit.")
	case errors.Is(err, upstream.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		problem(w, r, 504, "upstream_timeout", "The source did not respond in time.")
	case errors.Is(err, upstream.ErrNetwork), errors.Is(err, upstream.ErrRedirect):
		problem(w, r, 502, "upstream_unavailable", "The source could not be reached.")
	case errors.Is(err, profiles.ErrNotFound), errors.Is(err, auth.ErrNotFound):
		problem(w, r, 404, "not_found", "This item does not exist.")
	case errors.Is(err, profiles.ErrInvalid), errors.Is(err, auth.ErrInvalid):
		problem(w, r, 400, "invalid_request", "Check the supplied settings.")
	case errors.Is(err, profiles.ErrChanged):
		problem(w, r, 409, "invalid_state", "The profile changed. Refresh it before unlocking.")
	case errors.Is(err, profiles.ErrLocked):
		problem(w, r, 409, "credentials_required", "Unlock this profile before continuing.")
	default:
		problem(w, r, 500, "internal_error", "The request could not be completed.")
	}
}
