package content

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type deadlineResponse struct {
	mu            sync.Mutex
	header        http.Header
	deadline      time.Time
	changed       chan struct{}
	entered       chan struct{}
	once          sync.Once
	block         bool
	expired       bool
	lastFuture    time.Time
	futureUpdates int
}

func newDeadlineResponse(block bool) *deadlineResponse {
	return &deadlineResponse{header: make(http.Header), changed: make(chan struct{}), entered: make(chan struct{}), block: block}
}
func (w *deadlineResponse) Header() http.Header { return w.header }
func (w *deadlineResponse) WriteHeader(int)     {}
func (w *deadlineResponse) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = deadline
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.expired = true
	} else if !deadline.IsZero() {
		w.lastFuture = deadline
		w.futureUpdates++
	}
	close(w.changed)
	w.changed = make(chan struct{})
	return nil
}
func (w *deadlineResponse) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	if !w.block {
		return len(data), nil
	}
	for {
		w.mu.Lock()
		deadline, changed := w.deadline, w.changed
		w.mu.Unlock()
		if deadline.IsZero() {
			<-changed
			continue
		}
		delay := time.Until(deadline)
		if delay <= 0 {
			return 0, context.DeadlineExceeded
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			return 0, context.DeadlineExceeded
		case <-changed:
			timer.Stop()
		}
	}
}

func TestCancellationInterruptsBlockedBrowserWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newDeadlineResponse(true)
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	src := source(t)
	result := make(chan any, 1)
	go func() {
		defer func() { result <- recover() }()
		_ = serviceResponse(response(200, "body", nil)).Serve(w, r, src, false)
	}()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("browser write did not start")
	}
	cancel()
	select {
	case recovered := <-result:
		if recovered != http.ErrAbortHandler {
			t.Fatalf("cancelled stream did not abort: %v", recovered)
		}
	case <-time.After(time.Second):
		// Unblock the regression's worker so the failed test leaves no goroutine.
		_ = w.SetWriteDeadline(time.Now())
		<-result
		t.Fatal("cancellation left the browser write blocked")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.expired || !w.deadline.IsZero() {
		t.Fatalf("deadline not expired and cleared: expired=%v deadline=%v", w.expired, w.deadline)
	}
}

func TestStreamDeadlineInterruptsBlockedBrowserWrite(t *testing.T) {
	w := newDeadlineResponse(true)
	s := serviceResponse(response(200, "body", nil))
	s.limits.Timeout = 30 * time.Millisecond
	src := source(t)
	r := httptest.NewRequest("GET", "/", nil)
	result := make(chan any, 1)
	go func() { defer func() { result <- recover() }(); _ = s.Serve(w, r, src, false) }()
	select {
	case recovered := <-result:
		if recovered != http.ErrAbortHandler {
			t.Fatalf("timed out stream did not abort: %v", recovered)
		}
	case <-time.After(time.Second):
		_ = w.SetWriteDeadline(time.Now())
		<-result
		t.Fatal("stream timeout did not interrupt blocked write")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.deadline.IsZero() {
		t.Fatal("response deadline leaked after stream timeout")
	}
}

func TestSuccessfulStreamClearsBrowserDeadline(t *testing.T) {
	started := time.Now()
	w := newDeadlineResponse(false)
	if err := serviceResponse(response(200, "body", nil)).Serve(w, httptest.NewRequest("GET", "/", nil), source(t), false); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.deadline.IsZero() {
		t.Fatal("response deadline leaked after success")
	}
	if w.futureUpdates < 2 || w.lastFuture.Before(started.Add(20*time.Second)) || w.lastFuture.After(time.Now().Add(31*time.Second)) {
		t.Fatalf("per-write 30-second deadline was not refreshed: count=%d deadline=%v", w.futureUpdates, w.lastFuture)
	}
}
