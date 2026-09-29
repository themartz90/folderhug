package mediaworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRendererBoundsAndSafeArguments(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		_, err := renderWithRunner(context.Background(), "ffmpeg", "http://127.0.0.1:1/media/random", "mov", func(ctx context.Context, _ string, args []string, stdout, stderr io.Writer) error {
			joined := strings.Join(args, " ")
			for _, required := range []string{"-protocol_whitelist http,tcp,pipe", "-format_whitelist mov,matroska,webm,avi", "-enable_drefs 0", "-use_absolute_path 0", "-frames:v 1", "-threads 1"} {
				if !strings.Contains(joined, required) {
					t.Errorf("missing restriction %s", required)
				}
			}
			writer, limit := stdout, MaxJPEGBytes
			if stream == "stderr" {
				writer, limit = stderr, 64<<10
			}
			if _, err := writer.Write(make([]byte, limit+1)); !errors.Is(err, ErrOutputLimit) {
				t.Error("output overflow accepted")
			}
			if ctx.Err() == nil {
				t.Error("output overflow did not cancel command")
			}
			return ctx.Err()
		})
		if !errors.Is(err, ErrRender) {
			t.Fatal("unsafe renderer error")
		}
	}
	poster := testJPEG(t)
	got, err := renderWithRunner(context.Background(), "ffmpeg", "http://127.0.0.1:1/media/random", "matroska", func(_ context.Context, _ string, args []string, stdout, stderr io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "enable_drefs") {
			t.Error("MOV option used for Matroska")
		}
		_, err := stdout.Write(poster)
		return err
	})
	if err != nil || !bytes.Equal(got, poster) {
		t.Fatal("valid poster rejected")
	}
}

func TestRenderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := renderWithRunner(ctx, "ffmpeg", "http://127.0.0.1:1/media/random", "avi", func(ctx context.Context, _ string, _ []string, _ io.Writer, _ io.Writer) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, ErrRender) {
		t.Fatal(err)
	}
}

func TestCommandHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "media-worker-output-helper" {
		return
	}
	for {
		_, _ = os.Stdout.Write(make([]byte, 64<<10))
	}
}

func TestExecuteKillsProcessOnOutputLimit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stdout := &boundedWriter{limit: MaxJPEGBytes, cancel: cancel}
	start := time.Now()
	err = execute(ctx, executable, []string{"-test.run=^TestCommandHelper$", "--", "media-worker-output-helper"}, stdout, io.Discard)
	if err == nil || ctx.Err() == nil || stdout.total > MaxJPEGBytes || time.Since(start) > 2*time.Second {
		t.Fatalf("process was not promptly bounded: %v, %d bytes, %s", err, stdout.total, time.Since(start))
	}
}
