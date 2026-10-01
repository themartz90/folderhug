// Package mediabroker exposes one leased video to the isolated media worker.
package mediabroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/thumbnails"
	"github.com/themartz90/folderhug/internal/upstream"
)

var ErrUnavailable = errors.New("video poster is unavailable")

const byteBudget int64 = 32 << 20
const jobTimeout = 15 * time.Second

type Broker struct {
	profiles        *profiles.Service
	auth            *auth.Service
	catalog         *catalog.Service
	fetcher         catalog.Fetcher
	key             string
	mu              sync.Mutex
	active          *lease
	server          *http.Server
	listener        net.Listener
	lastClaim       time.Time
	resultUploading bool
}
type lease struct {
	id, token            string
	ctx                  context.Context
	cancel               context.CancelFunc
	root                 *upstream.Root
	path                 string
	credentials          *upstream.Credentials
	size                 int64
	etag                 string
	expires              time.Time
	ready, claimed, used bool
	consumed             int64
	transfer             chan struct{}
	result               chan []byte
}

func New(p *profiles.Service, a *auth.Service, c *catalog.Service, f catalog.Fetcher, key []byte) (*Broker, error) {
	if len(key) != 32 {
		return nil, ErrUnavailable
	}
	return &Broker{profiles: p, auth: a, catalog: c, fetcher: f, key: base64.RawURLEncoding.EncodeToString(key)}, nil
}

func (b *Broker) Start(socketPath string) error {
	if !filepath.IsAbs(socketPath) {
		return ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0770); err != nil {
		return ErrUnavailable
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return ErrUnavailable
		}
		connection, dialErr := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if dialErr == nil {
			connection.Close()
			return ErrUnavailable
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return ErrUnavailable
		}
		if err = os.Remove(socketPath); err != nil {
			return ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return ErrUnavailable
	}
	if err = os.Chmod(socketPath, 0660); err != nil {
		listener.Close()
		return ErrUnavailable
	}
	b.listener = listener
	b.server = &http.Server{Handler: b.Handler(), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() { _ = b.server.Serve(listener) }()
	return nil
}

func (b *Broker) Close() error {
	b.mu.Lock()
	if b.active != nil {
		b.active.cancel()
	}
	b.mu.Unlock()
	if b.server != nil {
		return b.server.Close()
	}
	return nil
}

func (b *Broker) Generate(parent context.Context, request thumbnails.VideoRequest) ([]byte, error) {
	principal, err := b.auth.Refresh(parent, request.Principal)
	if err != nil || !principal.Can(request.ProfileID, "content:read") {
		return nil, ErrUnavailable
	}
	entry, err := b.catalog.Entry(parent, request.ProfileID, profiles.LeaseID(principal), request.EntryID)
	if err != nil || entry.Category != "video" || entry.Kind != "file" {
		return nil, ErrUnavailable
	}
	profile, credentials, access, release, err := b.profiles.Access(parent, request.ProfileID, profiles.LeaseID(principal))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer release()
	if !profile.Previews || profile.Generation != request.Generation || listing.ID(profile.ID, profile.Generation, entry.EscapedPath) != entry.ID {
		return nil, ErrUnavailable
	}
	root, err := upstream.NewRoot(profile.RootURL, profile.AllowHTTP)
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(access, jobTimeout)
	defer cancel()
	expires, _ := ctx.Deadline()
	j := &lease{id: rand.Text(), token: rand.Text(), ctx: ctx, cancel: cancel, root: root, path: entry.EscapedPath, credentials: credentials, expires: expires, transfer: make(chan struct{}, 1), result: make(chan []byte, 1)}
	b.mu.Lock()
	if b.active != nil {
		b.mu.Unlock()
		return nil, ErrUnavailable
	}
	b.active = j
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.active == j {
			b.active = nil
		}
		b.mu.Unlock()
	}()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				live, err := b.auth.Refresh(ctx, principal)
				if err != nil || !live.Can(profile.ID, "content:read") {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	if err = b.probe(j); err != nil {
		return nil, ErrUnavailable
	}
	b.mu.Lock()
	j.ready = true
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		select {
		case data := <-j.result:
			if len(data) > 0 {
				return data, nil
			}
		default:
		}
		return nil, ErrUnavailable
	case data := <-j.result:
		if len(data) == 0 {
			return nil, ErrUnavailable
		}
		return data, nil
	}
}

func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /claim", b.claim)
	mux.HandleFunc("GET /jobs/{id}/content", b.file)
	mux.HandleFunc("POST /jobs/{id}/result", b.result)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func authorized(r *http.Request, token string) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+token)) == 1
}

func (b *Broker) claim(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, b.key) {
		w.WriteHeader(401)
		return
	}
	b.mu.Lock()
	b.lastClaim = time.Now()
	j := b.active
	if j == nil || !j.ready || j.claimed || j.ctx.Err() != nil {
		b.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	j.claimed = true
	response := map[string]any{"id": j.id, "token": j.token, "expires_at": j.expires, "size": j.size, "etag": j.etag}
	b.mu.Unlock()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (b *Broker) current(r *http.Request) *lease {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.active
	if j == nil || !j.claimed || j.used || j.ctx.Err() != nil || r.PathValue("id") != j.id || !authorized(r, j.token) {
		return nil
	}
	return j
}

func (b *Broker) result(w http.ResponseWriter, r *http.Request) {
	j := b.current(r)
	if j == nil {
		w.WriteHeader(401)
		return
	}
	b.mu.Lock()
	if b.active != j || j.used || j.ctx.Err() != nil {
		b.mu.Unlock()
		w.WriteHeader(401)
		return
	}
	// Limit result buffering across leases, including an expired upload still reading.
	if b.resultUploading {
		b.mu.Unlock()
		w.WriteHeader(429)
		return
	}
	b.resultUploading = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.resultUploading = false
		b.mu.Unlock()
	}()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(413)
		return
	}
	var result []byte
	switch r.Header.Get("Content-Type") {
	case "image/jpeg":
		if len(body) < 3 || body[0] != 0xff || body[1] != 0xd8 || body[2] != 0xff {
			w.WriteHeader(400)
			return
		}
		result = body
	case "application/json":
		var state struct {
			State string `json:"state"`
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if d.Decode(&state) != nil || d.Decode(new(any)) != io.EOF || state.State != "unavailable" {
			w.WriteHeader(400)
			return
		}
	default:
		w.WriteHeader(415)
		return
	}
	b.mu.Lock()
	if b.active != j || j.used || j.ctx.Err() != nil {
		b.mu.Unlock()
		w.WriteHeader(401)
		return
	}
	j.used = true
	j.result <- result
	j.cancel()
	b.mu.Unlock()
	w.WriteHeader(204)
}

func (b *Broker) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.lastClaim.IsZero() && time.Since(b.lastClaim) < 15*time.Second
}
