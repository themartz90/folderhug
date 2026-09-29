package mediaworker

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type bridge struct {
	ctx       context.Context
	cancel    context.CancelFunc
	client    *http.Client
	job       lease
	path      string
	server    *http.Server
	listener  net.Listener
	closeOnce sync.Once
	bytes     atomic.Int64
	slots     chan struct{}
}

func newBridge(ctx context.Context, client *http.Client, job lease) (*bridge, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, ErrRender
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &bridge{ctx: ctx, cancel: cancel, client: client, job: job, path: "/media/" + rand.Text(), listener: listener, slots: make(chan struct{}, 1)}
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 2e9, IdleTimeout: 2e9, MaxHeaderBytes: 8 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = b.server.Serve(listener) }()
	go func() { <-ctx.Done(); b.Close() }()
	return b, nil
}
func (b *bridge) URL() string { return "http://" + b.listener.Addr().String() + b.path }
func (b *bridge) Close() {
	b.closeOnce.Do(func() { b.cancel(); _ = b.server.Close(); _ = b.listener.Close() })
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != b.path || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.Host != b.listener.Addr().String() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if b.ctx.Err() != nil || !timeBeforeExpiry(b.job) {
		http.Error(w, "Media lease expired", 410)
		return
	}
	if r.Header.Get("If-Range") != "" && r.Header.Get("If-Range") != b.job.ETag {
		http.Error(w, "Invalid media range", 400)
		return
	}
	start, end, err := requestedRange(r.Header.Get("Range"), b.job.Size)
	if err != nil {
		http.Error(w, "Invalid media range", 416)
		return
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-r.Context().Done():
		return
	}
	response, err := b.content(r.Context(), r.Method, start, end)
	if err != nil {
		http.Error(w, "Media unavailable", 502)
		return
	}
	defer response.Body.Close()
	actualStart, actualEnd, err := validateResponse(response, b.job, start, end)
	if err != nil {
		http.Error(w, "Media unavailable", 502)
		return
	}
	length := actualEnd - actualStart + 1
	if r.Method == http.MethodGet && b.bytes.Add(length) > MaxInputBytes {
		b.cancel()
		http.Error(w, "Media budget exceeded", 429)
		return
	}
	for _, name := range []string{"Content-Length", "Content-Range", "ETag"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if _, err = io.CopyN(w, response.Body, length); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func (b *bridge) content(ctx context.Context, method string, start, end int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://broker/jobs/"+b.job.ID+"/content", nil)
	if err != nil {
		return nil, ErrBroker
	}
	req.Header.Set("Authorization", "Bearer "+b.job.Token)
	req.Header.Set("Range", "bytes="+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10))
	req.Header.Set("If-Range", b.job.ETag)
	response, err := b.client.Do(req)
	if err != nil {
		return nil, ErrBroker
	}
	return response, nil
}

func requestedRange(raw string, size int64) (int64, int64, error) {
	if raw == "" {
		return 0, size - 1, nil
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return 0, 0, ErrProtocol
	}
	startText, endText, ok := strings.Cut(strings.TrimPrefix(raw, "bytes="), "-")
	if !ok {
		return 0, 0, ErrProtocol
	}
	if startText == "" {
		suffix, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, ErrProtocol
		}
		return max(0, size-suffix), size - 1, nil
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, ErrProtocol
	}
	end := size - 1
	if endText != "" {
		end, err = strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return 0, 0, ErrProtocol
		}
		end = min(end, size-1)
	}
	return start, end, nil
}

func validateResponse(response *http.Response, job lease, start, end int64) (int64, int64, error) {
	if response.StatusCode != http.StatusPartialContent {
		return 0, 0, ErrProtocol
	}
	if response.Header.Get("ETag") != job.ETag || response.Header.Get("Content-Encoding") != "" {
		return 0, 0, ErrProtocol
	}
	raw := response.Header.Get("Content-Range")
	if !strings.HasPrefix(raw, "bytes ") {
		return 0, 0, ErrProtocol
	}
	span, totalText, ok := strings.Cut(strings.TrimPrefix(raw, "bytes "), "/")
	if !ok {
		return 0, 0, ErrProtocol
	}
	firstText, lastText, ok := strings.Cut(span, "-")
	if !ok {
		return 0, 0, ErrProtocol
	}
	first, e1 := strconv.ParseInt(firstText, 10, 64)
	last, e2 := strconv.ParseInt(lastText, 10, 64)
	total, e3 := strconv.ParseInt(totalText, 10, 64)
	length, e4 := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || first != start || last < first || last > end || total != job.Size || length != last-first+1 || length > MaxInputBytes {
		return 0, 0, ErrProtocol
	}
	return first, last, nil
}

func (b *bridge) format(ctx context.Context) (string, error) {
	response, err := b.content(ctx, http.MethodGet, 0, min(63, b.job.Size-1))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	first, last, err := validateResponse(response, b.job, 0, min(63, b.job.Size-1))
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, last-first+2))
	if err != nil || int64(len(body)) != last-first+1 {
		return "", ErrProtocol
	}
	b.bytes.Add(int64(len(body)))
	if len(body) >= 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "AVI " {
		return "avi", nil
	}
	if len(body) >= 4 && string(body[:4]) == "\x1a\x45\xdf\xa3" {
		return "matroska", nil
	}
	if len(body) >= 8 {
		switch string(body[4:8]) {
		case "ftyp", "moov", "wide", "free", "mdat":
			return "mov", nil
		}
	}
	return "", errors.New("media container is unsupported")
}
