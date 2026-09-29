package events

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
)

var (
	ErrForbidden            = errors.New("this principal cannot receive events")
	ErrStreamingUnsupported = errors.New("event streaming is unavailable")
	ErrBusy                 = errors.New("too many event streams")
)

const maxEvents = 1000
const maxSubscribers = 64

type Event struct {
	Type      string  `json:"type"`
	ObjectID  *string `json:"object_id"`
	ProfileID *string `json:"profile_id"`
	State     string  `json:"state"`
}

type record struct {
	id        string
	sequence  uint64
	created   time.Time
	creatorID string
	event     Event
}

type Hub struct {
	auth              *auth.Service
	mu                sync.Mutex
	records           []record
	sequence          uint64
	discarded         uint64
	subscribers       map[chan struct{}]struct{}
	now               func() time.Time
	retention         time.Duration
	refreshInterval   time.Duration
	heartbeatInterval time.Duration
	writeTimeout      time.Duration
}

func New(authentication *auth.Service) *Hub {
	return &Hub{auth: authentication, subscribers: make(map[chan struct{}]struct{}), now: time.Now, retention: 5 * time.Minute, refreshInterval: time.Second, heartbeatInterval: 15 * time.Second, writeTimeout: 5 * time.Second}
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validEvent(event Event) bool {
	if event.ObjectID == nil || event.ProfileID == nil || !safeID(*event.ObjectID) || !safeID(*event.ProfileID) {
		return false
	}
	switch event.Type {
	case "scan":
		switch event.State {
		case "queued", "running", "paused", "waiting_for_credentials", "interrupted", "completed", "cancelled", "failed", "partial":
			return true
		}
	case "download":
		switch event.State {
		case "queued", "running", "paused", "waiting_for_credentials", "interrupted", "completed", "cancelled", "failed":
			return true
		}
	case "thumbnail":
		switch event.State {
		case "pending", "ready", "unavailable", "disabled":
			return true
		}
	}
	return false
}

func (h *Hub) Publish(event Event, creatorID string) {
	if !validEvent(event) || !safeID(creatorID) {
		return
	}
	// Copy pointed-to IDs so callers cannot mutate retained notifications.
	objectID, profileID := *event.ObjectID, *event.ProfileID
	event.ObjectID = &objectID
	event.ProfileID = &profileID
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(h.now())
	h.sequence++
	h.records = append(h.records, record{id: rand.Text(), sequence: h.sequence, created: h.now(), creatorID: creatorID, event: event})
	h.prune(h.now())
	for wake := range h.subscribers {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) prune(now time.Time) {
	remove := 0
	for remove < len(h.records) && (len(h.records)-remove > maxEvents || !h.records[remove].created.Add(h.retention).After(now)) {
		remove++
	}
	if remove == 0 {
		return
	}
	h.discarded = h.records[remove-1].sequence
	copy(h.records, h.records[remove:])
	clear(h.records[len(h.records)-remove:])
	h.records = h.records[:len(h.records)-remove]
}

func visible(principal auth.Principal, item record) bool {
	if item.event.ProfileID == nil {
		return false
	}
	switch item.event.Type {
	case "scan":
		return principal.Can(*item.event.ProfileID, "browse:read")
	case "thumbnail":
		return principal.Can(*item.event.ProfileID, "content:read")
	case "download":
		return principal.CanJob(*item.event.ProfileID, item.creatorID, "downloads:write")
	}
	return false
}

func allowed(principal auth.Principal) bool {
	if principal.Owner {
		return true
	}
	for _, scope := range principal.Scopes {
		if scope == "browse:read" || scope == "content:read" || scope == "downloads:write" {
			return true
		}
	}
	return false
}

func (h *Hub) subscribe(lastID string, p auth.Principal) (chan struct{}, uint64, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) >= maxSubscribers {
		return nil, 0, false, ErrBusy
	}
	h.prune(h.now())
	cursor := h.sequence
	reset := false
	if lastID != "" {
		reset = true
		for _, item := range h.records {
			if item.id == lastID && visible(p, item) {
				cursor = item.sequence
				reset = false
				break
			}
		}
	}
	wake := make(chan struct{}, 1)
	h.subscribers[wake] = struct{}{}
	return wake, cursor, reset, nil
}

func (h *Hub) snapshot(cursor uint64) ([]record, uint64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(h.now())
	if cursor < h.discarded {
		return nil, h.sequence, true
	}
	out := make([]record, 0, len(h.records))
	for _, item := range h.records {
		if item.sequence > cursor {
			out = append(out, item)
		}
	}
	return out, h.sequence, false
}

// Serve returns errors only before response headers have been sent.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	p, err := h.auth.Refresh(r.Context(), p)
	if err != nil {
		return err
	}
	if !allowed(p) {
		return ErrForbidden
	}
	lastID := r.Header.Get("Last-Event-ID")
	if len(lastID) > 128 {
		return auth.ErrInvalid
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(h.writeTimeout)); err != nil {
		return ErrStreamingUnsupported
	}
	defer controller.SetWriteDeadline(time.Time{})
	wake, cursor, reset, err := h.subscribe(lastID, p)
	if err != nil {
		return err
	}
	defer func() { h.mu.Lock(); delete(h.subscribers, wake); h.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(text string) bool {
		if controller.SetWriteDeadline(time.Now().Add(h.writeTimeout)) != nil {
			return false
		}
		defer controller.SetWriteDeadline(time.Time{})
		if _, err := fmt.Fprint(w, text); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	writeEvent := func(id string, event Event) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		return write("id: " + id + "\nevent: " + event.Type + "\ndata: " + string(data) + "\n\n")
	}
	writeReset := func() bool { return writeEvent(rand.Text(), Event{Type: "reset", State: "refetch_required"}) }
	if !write("retry: 3000\n\n") {
		return nil
	}
	if reset && !writeReset() {
		return nil
	}
	refresh := time.NewTicker(h.refreshInterval)
	defer refresh.Stop()
	heartbeat := time.NewTicker(h.heartbeatInterval)
	defer heartbeat.Stop()
	drain := func() bool {
		var err error
		p, err = h.auth.Refresh(r.Context(), p)
		if err != nil || !allowed(p) {
			return false
		}
		items, next, missed := h.snapshot(cursor)
		if missed {
			if !writeReset() {
				return false
			}
		} else {
			checkedAt := time.Now()
			for _, item := range items {
				if time.Since(checkedAt) >= h.refreshInterval {
					p, err = h.auth.Refresh(r.Context(), p)
					if err != nil || !allowed(p) {
						return false
					}
					checkedAt = time.Now()
				}
				if visible(p, item) && !writeEvent(item.id, item.event) {
					return false
				}
			}
		}
		cursor = next
		return true
	}
	if !drain() {
		return nil
	}
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-wake:
			if !drain() {
				return nil
			}
		case <-refresh.C:
			p, err = h.auth.Refresh(r.Context(), p)
			if err != nil || !allowed(p) {
				return nil
			}
		case <-heartbeat.C:
			if !write(": keep-alive\n\n") {
				return nil
			}
		}
	}
}
