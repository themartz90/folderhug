package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/store"
)

type frame struct{ id, name, data, retry, comment string }
type stream struct {
	response *http.Response
	frames   chan frame
	done     chan struct{}
	cancel   context.CancelFunc
}

func eventFixture(t *testing.T) (*Hub, *store.Store, auth.Principal) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := auth.New(db.DB)
	token, raw, err := a.CreateToken(context.Background(), auth.TokenCreate{Name: "events", ProfileIDs: []string{"one"}, Scopes: []string{"browse:read", "content:read", "downloads:write"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(context.Background(), "", raw)
	if err != nil || p.CreatorID != token.ID {
		t.Fatal(p, err)
	}
	h := New(a)
	h.refreshInterval = 10 * time.Millisecond
	h.heartbeatInterval = time.Hour
	return h, db, p
}

func openStream(t *testing.T, h *Hub, p auth.Principal, lastID string) *stream {
	t.Helper()
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if err := h.Serve(w, r, p); err != nil {
			http.Error(w, err.Error(), 401)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		request.Header.Set("Last-Event-ID", lastID)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		cancel()
		server.Close()
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-store" {
		response.Body.Close()
		cancel()
		server.Close()
		t.Fatal("invalid stream headers", response.StatusCode)
	}
	s := &stream{response: response, frames: make(chan frame, 16), done: done, cancel: cancel}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(s.frames)
		scan := bufio.NewScanner(response.Body)
		var f frame
		for scan.Scan() {
			line := scan.Text()
			if line == "" {
				select {
				case s.frames <- f:
				case <-ctx.Done():
					return
				}
				f = frame{}
				continue
			}
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, "retry: "):
				f.retry = strings.TrimPrefix(line, "retry: ")
			case strings.HasPrefix(line, ":"):
				f.comment = line
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		response.Body.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("event handler did not stop")
		}
		server.Close()
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
			t.Error("event reader did not stop")
		}
	})
	if f := nextFrame(t, s); f.retry != "3000" {
		t.Fatal("missing reconnect delay", f)
	}
	return s
}

func nextFrame(t *testing.T, s *stream) frame {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("stream closed before event")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("event did not arrive")
	}
	return frame{}
}

func makeEvent(kind, id, profile, state string) Event {
	return Event{Type: kind, ObjectID: &id, ProfileID: &profile, State: state}
}
func requireObject(t *testing.T, f frame, want string) {
	t.Helper()
	var e Event
	if err := json.Unmarshal([]byte(f.data), &e); err != nil {
		t.Fatal(err)
	}
	if e.ObjectID == nil || *e.ObjectID != want || f.id == "" || e.Type != f.name {
		t.Fatal("unexpected event", f)
	}
}

func TestLiveStreamFiltersProfilesScopesAndJobOwnership(t *testing.T) {
	h, _, p := eventFixture(t)
	s := openStream(t, h, p, "")
	h.Publish(makeEvent("scan", "foreign", "two", "running"), "other")
	h.Publish(makeEvent("download", "another-job", "one", "running"), "other")
	h.Publish(makeEvent("scan", "scan-one", "one", "partial"), "other")
	requireObject(t, nextFrame(t, s), "scan-one")
	h.Publish(makeEvent("download", "own-job", "one", "running"), p.CreatorID)
	requireObject(t, nextFrame(t, s), "own-job")
	h.Publish(makeEvent("thumbnail", "poster", "one", "ready"), "other")
	requireObject(t, nextFrame(t, s), "poster")
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop handler")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) != 0 {
		t.Fatal("subscriber leaked after cancellation")
	}
}

func TestReplayAndUnknownCursorReset(t *testing.T) {
	h, _, p := eventFixture(t)
	s := openStream(t, h, p, "")
	h.Publish(makeEvent("scan", "first", "one", "running"), "other")
	first := nextFrame(t, s)
	requireObject(t, first, "first")
	s.cancel()
	<-s.done
	h.Publish(makeEvent("scan", "hidden", "two", "running"), "other")
	h.Publish(makeEvent("scan", "second", "one", "completed"), "other")
	replay := openStream(t, h, p, first.id)
	requireObject(t, nextFrame(t, replay), "second")
	unknown := openStream(t, h, p, "unrecognized-cursor")
	reset := nextFrame(t, unknown)
	if reset.name != "reset" || reset.id == "" || reset.data != `{"type":"reset","object_id":null,"profile_id":null,"state":"refetch_required"}` {
		t.Fatal("invalid reset", reset)
	}
	h.Publish(makeEvent("scan", "third", "one", "running"), "other")
	requireObject(t, nextFrame(t, unknown), "third")
	h.mu.Lock()
	foreignID := h.records[1].id
	h.mu.Unlock()
	foreign := openStream(t, h, p, foreignID)
	if f := nextFrame(t, foreign); f.name != "reset" {
		t.Fatal("foreign cursor disclosed retained history", f)
	}
}

func TestStreamsCloseOnRevocationExpiryAndPermissionLoss(t *testing.T) {
	for _, action := range []string{"revoke", "expire", "scopes"} {
		t.Run(action, func(t *testing.T) {
			h, db, p := eventFixture(t)
			s := openStream(t, h, p, "")
			switch action {
			case "revoke":
				if err := h.auth.DeleteToken(context.Background(), p.CreatorID); err != nil {
					t.Fatal(err)
				}
			case "expire":
				if _, err := db.DB.Exec(`UPDATE api_tokens SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), p.CreatorID); err != nil {
					t.Fatal(err)
				}
			case "scopes":
				if _, err := db.DB.Exec(`UPDATE api_tokens SET scopes='["credentials:unlock"]' WHERE id=?`, p.CreatorID); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				t.Fatal("revoked or expired stream remained open")
			}
		})
	}
}

func TestPermissionRefreshAppliesBeforeEventDelivery(t *testing.T) {
	h, db, p := eventFixture(t)
	s := openStream(t, h, p, "")
	if _, err := db.DB.Exec(`UPDATE api_tokens SET scopes='["browse:read"]',profile_ids='["two"]' WHERE id=?`, p.CreatorID); err != nil {
		t.Fatal(err)
	}
	h.Publish(makeEvent("scan", "old-profile", "one", "running"), "other")
	h.Publish(makeEvent("thumbnail", "forbidden-poster", "two", "ready"), "other")
	h.Publish(makeEvent("scan", "new-profile", "two", "running"), "other")
	requireObject(t, nextFrame(t, s), "new-profile")
}

func TestHistoryAndSlowSubscriberAreBounded(t *testing.T) {
	h, _, p := eventFixture(t)
	wake, cursor, _, err := h.subscribe("", p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxEvents+5; i++ {
		h.Publish(makeEvent("scan", "fixture", "one", "running"), "other")
	}
	if len(wake) != 1 {
		t.Fatal("notifications were not coalesced")
	}
	items, next, reset := h.snapshot(cursor)
	if !reset || len(items) != 0 || next != maxEvents+5 {
		t.Fatal("slow subscriber did not require refetch")
	}
	h.mu.Lock()
	if len(h.records) != maxEvents {
		t.Fatal("history exceeded retention bound")
	}
	oldID := h.records[0].id
	h.mu.Unlock()
	future := time.Now().Add(6 * time.Minute)
	h.now = func() time.Time { return future }
	_, _, reset, err = h.subscribe(oldID, p)
	if err != nil || !reset {
		t.Fatal("expired cursor did not reset", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 0 {
		t.Fatal("expired history retained")
	}
}

func TestInvalidEventsAndCallerMutationCannotLeakData(t *testing.T) {
	h, _, p := eventFixture(t)
	s := openStream(t, h, p, "")
	h.Publish(makeEvent("scan", "valid", "one", "private-filename.mkv"), "other")
	h.Publish(makeEvent("scan", "private/file.txt", "one", "running"), "other")
	h.Publish(Event{Type: "reset", State: "refetch_required"}, "other")
	event := makeEvent("scan", "kept", "one", "running")
	h.Publish(event, "other")
	*event.ObjectID = "mutated"
	*event.ProfileID = "two"
	requireObject(t, nextFrame(t, s), "kept")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 1 {
		t.Fatal("invalid events retained")
	}
}

func TestHeartbeatAndPreheaderAuthentication(t *testing.T) {
	h, _, p := eventFixture(t)
	h.heartbeatInterval = 10 * time.Millisecond
	s := openStream(t, h, p, "")
	if f := nextFrame(t, s); f.comment != ": keep-alive" {
		t.Fatal("missing heartbeat", f)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/events", nil)
	if err := h.Serve(w, request, auth.Principal{}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("unauthenticated stream accepted", err)
	}
	if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatal("preheader authentication failure wrote response")
	}
	if err := h.Serve(w, request, p); !errors.Is(err, ErrStreamingUnsupported) {
		t.Fatal("writer without deadlines accepted", err)
	}
}

func TestOwnerStreamSeesAllProfilesAndClosesOnLogout(t *testing.T) {
	h, _, _ := eventFixture(t)
	ctx := context.Background()
	if err := h.auth.Setup(ctx, "synthetic owner password"); err != nil {
		t.Fatal(err)
	}
	raw, _, err := h.auth.Login(ctx, "synthetic owner password")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.auth.Authenticate(ctx, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	s := openStream(t, h, p, "")
	h.Publish(makeEvent("download", "other-job", "another-profile", "completed"), "other-token")
	requireObject(t, nextFrame(t, s), "other-job")
	if err := h.auth.RevokeSession(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("logged-out owner stream remained open")
	}
}

func TestAdmissionAndReplayCursorLimits(t *testing.T) {
	h, _, p := eventFixture(t)
	for i := 0; i < maxSubscribers; i++ {
		if _, _, _, err := h.subscribe("", p); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := h.subscribe("", p); !errors.Is(err, ErrBusy) {
		t.Fatal("unbounded subscriber admission", err)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/events", nil)
	request.Header.Set("Last-Event-ID", strings.Repeat("a", 129))
	if err := h.Serve(w, request, p); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("oversized cursor accepted", err)
	}
	_, raw, err := h.auth.CreateToken(context.Background(), auth.TokenCreate{Name: "unlock only", ProfileIDs: []string{"one"}, Scopes: []string{"credentials:unlock"}})
	if err != nil {
		t.Fatal(err)
	}
	unlockOnly, err := h.auth.Authenticate(context.Background(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Serve(w, httptest.NewRequest(http.MethodGet, "/events", nil), unlockOnly); !errors.Is(err, ErrForbidden) {
		t.Fatal("unscoped event access accepted", err)
	}
}
