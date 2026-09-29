package mediaworker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFFmpegIntegration(t *testing.T) {
	if os.Getenv("MEDIAWORKER_FFMPEG_INTEGRATION") != "1" {
		t.Skip("set MEDIAWORKER_FFMPEG_INTEGRATION=1 for installed FFmpeg checks")
	}
	executable := os.Getenv("FFMPEG_PATH")
	if executable == "" {
		executable = "ffmpeg"
	}
	version, err := exec.Command(executable, "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.SplitN(string(version), "\n", 2)[0])
	for _, tc := range []struct {
		name, format, codec string
		extra               []string
	}{
		{"faststart.mp4", "mov", "mpeg4", []string{"-movflags", "+faststart"}},
		{"moov-at-end.mp4", "mov", "mpeg4", nil},
		{"sample.mkv", "matroska", "mpeg4", nil},
		{"sample.avi", "avi", "mpeg4", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=2", "-t", "1", "-an", "-c:v", tc.codec, "-threads", "1"}
			args = append(args, tc.extra...)
			args = append(args, path)
			if output, err := exec.Command(executable, args...).CombinedOutput(); err != nil {
				t.Fatalf("synthetic fixture: %v %s", err, output)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			job := testLease()
			job.Size = int64(len(data))
			var fetched int64
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "broker" || r.Header.Get("Authorization") != "Bearer "+job.Token {
					t.Error("wrong content capability")
				}
				start, end, err := requestedRange(r.Header.Get("Range"), job.Size)
				if err != nil {
					return nil, err
				}
				body := data[start : end+1]
				fetched += int64(len(body))
				response := &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
				response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, job.Size))
				response.Header.Set("Content-Length", fmt.Sprint(len(body)))
				response.Header.Set("ETag", job.ETag)
				return response, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			b, err := newBridge(ctx, client, job)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			format, err := b.format(ctx)
			if err != nil || format != tc.format {
				t.Fatalf("format %q: %v", format, err)
			}
			poster, err := renderFFmpeg(ctx, executable, b.URL(), format)
			if err != nil || !validJPEG(poster) {
				t.Fatalf("render failed: %v", err)
			}
			if fetched > MaxInputBytes {
				t.Fatal("input budget exceeded")
			}
			t.Logf("source=%d fetched=%d JPEG=%d", len(data), fetched, len(poster))
		})
	}
}
