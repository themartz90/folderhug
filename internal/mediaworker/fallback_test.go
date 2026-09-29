package mediaworker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fallbackResult struct{ contentType, body string }

type fallbackBroker struct {
	job      lease
	path     string
	consumed atomic.Int64
	results  chan fallbackResult
}

type fallbackBody struct {
	ctx      context.Context
	file     *os.File
	reader   *io.SectionReader
	consumed *atomic.Int64
}

func (b *fallbackBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := b.reader.Read(p)
	b.consumed.Add(int64(n))
	return n, err
}
func (b *fallbackBody) Close() error { return b.file.Close() }

func newFallbackBroker(t *testing.T, path string) (*fallbackBroker, *http.Client) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatal("fixture must be a nonempty regular file")
	}
	b := &fallbackBroker{job: testLease(), path: path, results: make(chan fallbackResult, 1)}
	b.job.Size = info.Size()
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		empty := func(status int) *http.Response {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		}
		if r.URL.Host != "broker" || r.Header.Get("Authorization") != "Bearer "+b.job.Token {
			t.Error("fixture request escaped fixed broker capability")
			return empty(401), nil
		}
		if r.URL.Path == "/jobs/"+b.job.ID+"/result" && r.Method == http.MethodPost {
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxJPEGBytes+1))
			if err != nil {
				return nil, err
			}
			if len(body) > MaxJPEGBytes {
				t.Error("result exceeded output cap")
			}
			select {
			case b.results <- fallbackResult{r.Header.Get("Content-Type"), string(body)}:
			default:
				t.Error("duplicate result")
			}
			return empty(204), nil
		}
		if r.URL.Path != "/jobs/"+b.job.ID+"/content" || (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("If-Range") != b.job.ETag {
			t.Error("fixture content request changed binding")
			return empty(400), nil
		}
		remaining := int64(MaxInputBytes) - b.consumed.Load()
		if remaining <= 0 {
			return empty(413), nil
		}
		start, end, err := requestedRange(r.Header.Get("Range"), b.job.Size)
		if err != nil {
			return empty(416), nil
		}
		end = min(end, start+remaining-1)
		response := empty(206)
		response.ContentLength = end - start + 1
		response.Header.Set("Content-Length", fmt.Sprint(response.ContentLength))
		response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, b.job.Size))
		response.Header.Set("ETag", b.job.ETag)
		if r.Method == http.MethodGet {
			file, err := os.Open(b.path)
			if err != nil {
				return nil, err
			}
			response.Body = &fallbackBody{ctx: r.Context(), file: file, reader: io.NewSectionReader(file, start, response.ContentLength), consumed: &b.consumed}
		}
		return response, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return b, client
}

func assertFallback(t *testing.T, b *fallbackBroker, elapsed time.Duration) {
	t.Helper()
	select {
	case result := <-b.results:
		if result.contentType != "application/json" || result.body != `{"state":"unavailable"}` {
			t.Fatalf("unexpected fallback result: content type %q, %d bytes", result.contentType, len(result.body))
		}
	default:
		t.Fatal("missing unavailable result")
	}
	if elapsed > MaxJobTime+3*time.Second {
		t.Fatalf("fallback exceeded render and delivery deadlines: %s", elapsed)
	}
	if consumed := b.consumed.Load(); consumed <= 0 || consumed > MaxInputBytes {
		t.Fatalf("input budget: %d", consumed)
	}
	t.Logf("source=%d consumed=%d elapsed=%s", b.job.Size, b.consumed.Load(), elapsed)
}

func TestFFmpegFixtureFallback(t *testing.T) {
	directory := os.Getenv("MEDIAWORKER_FIXTURE_DIR")
	if directory == "" {
		t.Skip("set MEDIAWORKER_FIXTURE_DIR to generated synthetic fixtures for installed FFmpeg fallback checks")
	}
	executable := os.Getenv("FFMPEG_PATH")
	if executable == "" {
		executable = "ffmpeg"
	}
	versionContext, stopVersion := context.WithTimeout(context.Background(), 3*time.Second)
	version, versionError := exec.CommandContext(versionContext, executable, "-version").Output()
	stopVersion()
	if versionError != nil {
		t.Fatalf("installed FFmpeg is required: %v", versionError)
	}
	t.Log(strings.SplitN(string(version), "\n", 2)[0])
	for _, name := range []string{"over-budget.avi", "corrupt.mp4", "playlist.m3u8"} {
		t.Run(name, func(t *testing.T) {
			b, client := newFallbackBroker(t, filepath.Join(directory, name))
			if name == "over-budget.avi" && b.job.Size <= MaxInputBytes {
				t.Fatal("oversized fixture is too small")
			}
			ctx, cancel := context.WithTimeout(context.Background(), MaxJobTime+3*time.Second)
			defer cancel()
			var renders atomic.Int64
			w := worker{client: client, render: func(ctx context.Context, input, format string) ([]byte, error) {
				renders.Add(1)
				return renderFFmpeg(ctx, executable, input, format)
			}}
			start := time.Now()
			w.process(ctx, b.job)
			assertFallback(t, b, time.Since(start))
			if name == "playlist.m3u8" {
				if renders.Load() != 0 {
					t.Fatal("playlist reached FFmpeg")
				}
			} else if renders.Load() != 1 {
				t.Fatal("fixture did not exercise actual FFmpeg")
			}
		})
	}
	t.Run("forced-format-playlist", func(t *testing.T) {
		var hits atomic.Int64
		trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(500) }))
		defer trap.Close()
		path := writeTrapPlaylist(t, trap.URL)
		for _, format := range []string{"mov", "matroska", "avi"} {
			b, client := newFallbackBroker(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			bridge, err := newBridge(ctx, client, b.job)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			start := time.Now()
			poster, err := renderFFmpeg(ctx, executable, bridge.URL(), format)
			bridge.Close()
			cancel()
			if err == nil || len(poster) != 0 {
				t.Fatalf("playlist accepted as %s", format)
			}
			if time.Since(start) > 4*time.Second || b.consumed.Load() > MaxInputBytes {
				t.Fatalf("unbounded forced-format %s", format)
			}
		}
		if hits.Load() != 0 {
			t.Fatalf("FFmpeg fetched playlist target %d times", hits.Load())
		}
	})
}

func writeTrapPlaylist(t *testing.T, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "playlist.m3u8")
	body := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + target + "/private.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlaylistSniffRejectsExternalTarget(t *testing.T) {
	var hits atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer trap.Close()
	b, client := newFallbackBroker(t, writeTrapPlaylist(t, trap.URL))
	w := worker{client: client, render: func(context.Context, string, string) ([]byte, error) {
		t.Error("playlist reached renderer")
		return nil, ErrRender
	}}
	start := time.Now()
	w.process(context.Background(), b.job)
	assertFallback(t, b, time.Since(start))
	if hits.Load() != 0 {
		t.Fatal("playlist sniff fetched external target")
	}
	if b.consumed.Load() > 64 {
		t.Fatal("playlist rejection exceeded sniff bound")
	}
}
