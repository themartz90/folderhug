package content

import (
	"context"
	"net/http"
)

type responseWriter struct {
	http.ResponseWriter
	bounded *deadlineWriter
}

func (w responseWriter) Write(b []byte) (int, error) { return w.bounded.Write(b) }
func (w responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ResponseWriter applies the same cancellation and idle-write limits to local
// file responses and exports. Call finish before the handler returns.
func ResponseWriter(ctx context.Context, w http.ResponseWriter) (writer http.ResponseWriter, finish func(), err error) {
	bounded, err := newDeadlineWriter(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	return responseWriter{w, bounded}, bounded.finish, nil
}
