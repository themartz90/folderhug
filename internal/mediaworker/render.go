package mediaworker

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

func timeBeforeExpiry(job lease) bool { return time.Now().Before(job.ExpiresAt) }

func ffmpegArgs(input, format string) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-max_alloc", "16777216", "-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1", "-protocol_whitelist", "http,tcp,pipe", "-format_whitelist", "mov,matroska,webm,avi", "-probesize", "1048576", "-analyzeduration", "1000000", "-rw_timeout", "15000000", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	return append(args, "-i", input, "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1", "-vf", "scale=320:320:force_original_aspect_ratio=decrease:flags=bilinear", "-threads", "1", "-c:v", "mjpeg", "-q:v", "4", "-f", "image2pipe", "pipe:1")
}

func renderFFmpeg(parent context.Context, executable, input, format string) ([]byte, error) {
	return renderWithRunner(parent, executable, input, format, execute)
}

type commandRunner func(context.Context, string, []string, io.Writer, io.Writer) error

func renderWithRunner(parent context.Context, executable, input, format string, run commandRunner) ([]byte, error) {
	if format != "mov" && format != "matroska" && format != "avi" {
		return nil, ErrRender
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stdout := &boundedWriter{limit: MaxJPEGBytes, cancel: cancel}
	stderr := &boundedWriter{limit: 64 << 10, cancel: cancel, discard: true}
	if err := run(ctx, executable, ffmpegArgs(input, format), stdout, stderr); err != nil {
		return nil, ErrRender
	}
	if ctx.Err() != nil || !validJPEG(stdout.buffer.Bytes()) {
		return nil, ErrRender
	}
	return stdout.buffer.Bytes(), nil
}

func execute(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

type boundedWriter struct {
	mu           sync.Mutex
	buffer       bytes.Buffer
	limit, total int
	cancel       context.CancelFunc
	discard      bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > w.limit-w.total {
		w.cancel()
		return 0, ErrOutputLimit
	}
	w.total += len(p)
	if w.discard {
		return len(p), nil
	}
	return w.buffer.Write(p)
}

func validJPEG(body []byte) bool {
	if len(body) == 0 || len(body) > MaxJPEGBytes {
		return false
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 320 || config.Height > 320 {
		return false
	}
	_, err = jpeg.Decode(io.LimitReader(bytes.NewReader(body), MaxJPEGBytes))
	return err == nil
}
