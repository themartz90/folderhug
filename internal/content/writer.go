package content

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const browserWriteTimeout = 30 * time.Second

type deadlineWriter struct {
	ctx          context.Context
	writer       http.ResponseWriter
	controller   *http.ResponseController
	mu           sync.Mutex
	stop         func() bool
	callbackDone chan struct{}
	supported    bool
}

func newDeadlineWriter(ctx context.Context, writer http.ResponseWriter) (*deadlineWriter, error) {
	w := &deadlineWriter{ctx: ctx, writer: writer, controller: http.NewResponseController(writer), callbackDone: make(chan struct{})}
	err := w.controller.SetWriteDeadline(w.nextDeadline())
	if errors.Is(err, http.ErrNotSupported) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	w.supported = true
	w.stop = context.AfterFunc(ctx, func() {
		w.mu.Lock()
		_ = w.controller.SetWriteDeadline(time.Now())
		w.mu.Unlock()
		close(w.callbackDone)
	})
	return w, nil
}

func (w *deadlineWriter) nextDeadline() time.Time {
	deadline := time.Now().Add(browserWriteTimeout)
	if end, ok := w.ctx.Deadline(); ok && end.Before(deadline) {
		return end
	}
	return deadline
}

func (w *deadlineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if err := w.ctx.Err(); err != nil {
		w.mu.Unlock()
		return 0, err
	}
	if w.supported {
		if err := w.controller.SetWriteDeadline(w.nextDeadline()); err != nil {
			w.mu.Unlock()
			return 0, err
		}
	}
	w.mu.Unlock()
	return w.writer.Write(data)
}

func (w *deadlineWriter) finish() {
	if !w.supported {
		return
	}
	// Wait for an already running cancellation callback before clearing the
	// deadline, so it cannot poison the connection after this handler returns.
	if !w.stop() {
		<-w.callbackDone
	}
	w.mu.Lock()
	_ = w.controller.SetWriteDeadline(time.Time{})
	w.mu.Unlock()
}
