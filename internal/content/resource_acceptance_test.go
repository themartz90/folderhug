package content

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/testresource"
)

type resourceBody struct{ generatedBody }

func (b *resourceBody) Read(p []byte) (int, error) {
	n, err := b.generatedBody.Read(p)
	clear(p[:n])
	return n, err
}

func TestResourceGiBStreamRSS(t *testing.T) {
	if os.Getenv("HTTPWRAPPER_RESOURCE_ACCEPTANCE") != "1" || runtime.GOOS != "linux" {
		t.Skip("set HTTPWRAPPER_RESOURCE_ACCEPTANCE=1 on Linux for procfs resource measurements")
	}
	const length int64 = 1 << 30
	body := &resourceBody{generatedBody{remaining: length}}
	res := response(http.StatusOK, "", nil)
	res.Body, res.ContentLength = body, length
	s := serviceResponse(res)
	src := source(t)
	completed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := s.Serve(w, r, src, false)
		completed <- err
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 60 * time.Second
	runtime.GC()
	var received int64
	started := time.Now()
	measured, err := testresource.MeasureRSS(2*time.Millisecond, func() error {
		response, err := client.Get(server.URL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || response.ContentLength != length {
			return fmt.Errorf("stream status=%d length=%d", response.StatusCode, response.ContentLength)
		}
		received, err = io.CopyBuffer(io.Discard, response.Body, make([]byte, 32<<10))
		if err != nil {
			return err
		}
		select {
		case err := <-completed:
			return err
		case <-time.After(5 * time.Second):
			return fmt.Errorf("content service did not finish after the client reached EOF")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if received != length || body.remaining != 0 || body.maxRead > 32<<10 || !body.closed {
		t.Fatalf("incorrect stream: received=%d remaining=%d max_read=%d closed=%v", received, body.remaining, body.maxRead, body.closed)
	}
	growth := measured.Peak - measured.Initial
	t.Logf("process=test-harness content_bytes=%d duration=%s initial_rss_bytes=%d sampled_peak_rss_bytes=%d final_rss_bytes=%d rss_growth_bytes=%d samples=%d sample_interval_ms=2 target_growth_bytes=%d downstream=loopback-http upstream=generated-stream includes_client=true", received, time.Since(started), measured.Initial, measured.Peak, measured.Final, growth, measured.Samples, 64<<20)
	if measured.Samples < 3 {
		t.Fatal("transfer finished before any periodic RSS sample")
	}
	if growth >= 64<<20 {
		t.Fatalf("stream process RSS grew by %d bytes, reaching the 64 MiB target", growth)
	}
}
