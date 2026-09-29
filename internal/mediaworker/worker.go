package mediaworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxJPEGBytes  = 1 << 20
	MaxInputBytes = 32 << 20
	MaxJobTime    = 15 * time.Second
)

var (
	ErrConfig      = errors.New("media worker configuration is invalid")
	ErrBroker      = errors.New("media broker request failed")
	ErrProtocol    = errors.New("media broker returned an invalid response")
	ErrRender      = errors.New("media poster is unavailable")
	ErrOutputLimit = errors.New("media output limit exceeded")
)

type Config struct{ SocketPath, KeyPath, FFmpegPath string }
type lease struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Size      int64     `json:"size"`
	ETag      string    `json:"etag"`
}
type renderer func(context.Context, string, string) ([]byte, error)
type worker struct {
	client *http.Client
	key    string
	render renderer
	poll   time.Duration
}

func Run(ctx context.Context, config Config) error {
	if !filepath.IsAbs(config.SocketPath) || !filepath.IsAbs(config.KeyPath) || strings.ContainsRune(config.SocketPath, 0) {
		return ErrConfig
	}
	info, err := os.Lstat(config.KeyPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 32 {
		return ErrConfig
	}
	key, err := os.ReadFile(config.KeyPath)
	if err != nil || len(key) != 32 {
		return ErrConfig
	}
	if config.FFmpegPath == "" {
		config.FFmpegPath = "ffmpeg"
	}
	client, transport := unixClient(config.SocketPath)
	defer transport.CloseIdleConnections()
	w := worker{client: client, key: base64.RawURLEncoding.EncodeToString(key), render: func(ctx context.Context, input, format string) ([]byte, error) {
		return renderFFmpeg(ctx, config.FFmpegPath, input, format)
	}, poll: 500 * time.Millisecond}
	return w.run(ctx)
}

func unixClient(socket string) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		},
		DisableCompression:     true,
		MaxConnsPerHost:        2,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    2,
		ResponseHeaderTimeout:  2 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		IdleConnTimeout:        30 * time.Second,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport
}

func (w *worker) run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		job, err := w.claim(ctx)
		if errors.Is(err, ErrProtocol) {
			return err
		}
		if err == nil && job != nil {
			w.process(ctx, *job)
		}
		if ctx.Err() != nil {
			return nil
		}
		timer := time.NewTimer(w.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (w *worker) request(ctx context.Context, method, path, token, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://broker"+path, body)
	if err != nil {
		return nil, ErrBroker
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := w.client.Do(req)
	if err != nil {
		return nil, ErrBroker
	}
	return response, nil
}

func (w *worker) claim(ctx context.Context) (*lease, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := w.request(ctx, http.MethodPost, "/claim", w.key, "", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrBroker
	}
	reader := io.LimitReader(response.Body, 4097)
	body, err := io.ReadAll(reader)
	if err != nil || len(body) > 4096 {
		return nil, ErrProtocol
	}
	var job lease
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&job) != nil || decoder.Decode(new(any)) != io.EOF || !safeID(job.ID) || !safeToken(job.Token) || job.Size <= 0 || job.ExpiresAt.IsZero() || !strongETag(job.ETag) {
		return nil, ErrProtocol
	}
	return &job, nil
}

func safeID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func safeToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) >= 16 && len(decoded) <= 128
}
func strongETag(value string) bool {
	if len(value) < 2 || len(value) > 1024 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, c := range []byte(value[1 : len(value)-1]) {
		if c < 0x21 || c == '"' || c == 0x7f {
			return false
		}
	}
	return true
}

func (w *worker) process(parent context.Context, job lease) {
	deadline := time.Now().Add(MaxJobTime)
	if job.ExpiresAt.Before(deadline) {
		deadline = job.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	bridge, err := newBridge(ctx, w.client, job)
	if err != nil {
		w.result(parent, job, nil)
		return
	}
	defer bridge.Close()
	format, err := bridge.format(ctx)
	var poster []byte
	if err == nil {
		poster, err = w.render(ctx, bridge.URL(), format)
	}
	if err != nil || !validJPEG(poster) {
		poster = nil
	}
	bridge.Close()
	// Result delivery is bounded separately; the broker remains authoritative
	// about whether the capability has already expired.
	w.result(parent, job, poster)
}

func (w *worker) result(parent context.Context, job lease, poster []byte) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	body := poster
	contentType := "image/jpeg"
	if len(poster) == 0 {
		body = []byte(`{"state":"unavailable"}`)
		contentType = "application/json"
	}
	response, err := w.request(ctx, http.MethodPost, "/jobs/"+job.ID+"/result", job.Token, contentType, bytes.NewReader(body))
	if err != nil {
		return
	}
	response.Body.Close()
}
