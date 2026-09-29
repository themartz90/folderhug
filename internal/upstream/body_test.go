package upstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCompletedBodyKeepsEOF(t *testing.T) {
	for _, limit := range []int64{5, 20} {
		ctx, cancel := context.WithCancel(context.Background())
		body := &boundedBody{body: io.NopCloser(strings.NewReader("small")), remaining: limit, ctx: ctx, finish: cancel}
		data, err := io.ReadAll(body)
		if err != nil || string(data) != "small" {
			t.Fatalf("read: %q %v", data, err)
		}
		var b [1]byte
		if n, err := body.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("second read=%d %v", n, err)
		}
		if ctx.Err() == nil {
			t.Fatal("EOF did not release request")
		}
	}
}
