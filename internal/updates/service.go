package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/themartz90/folderhug/internal/buildinfo"
	"github.com/themartz90/folderhug/internal/upstream"
)

type Client interface {
	Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)
}
type LatestRelease struct {
	Version     string `json:"version"`
	ReleaseURL  string `json:"release_url"`
	PublishedAt string `json:"published_at"`
}
type Status struct {
	State     string         `json:"state"`
	Latest    *LatestRelease `json:"latest"`
	CheckedAt *time.Time     `json:"checked_at"`
	Stale     bool           `json:"stale"`
	RetryAt   *time.Time     `json:"retry_at"`
}

const maxBody = 1 << 20
const requestTimeout = 5 * time.Second

type Service struct {
	client      Client
	current     buildinfo.Info
	enabled     bool
	now         func() time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	latest      *LatestRelease
	checkedAt   time.Time
	nextAttempt time.Time
	failed      bool
	etag        string
	inflight    chan struct{}
}

func New(client Client, current buildinfo.Info, enabled bool) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{client: client, current: current, enabled: enabled, now: time.Now, ctx: ctx, cancel: cancel}
}

func (s *Service) Close() { s.cancel() }

func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	if !s.enabled {
		s.mu.Unlock()
		return Status{State: "disabled"}
	}
	if _, ok := buildinfo.ParseVersion(s.current.Version); !ok || s.current.Channel == "development" {
		s.mu.Unlock()
		return Status{State: "development"}
	}
	if s.ctx.Err() != nil || ctx.Err() != nil || s.now().Before(s.nextAttempt) {
		got := s.snapshot()
		s.mu.Unlock()
		return got
	}
	if s.inflight == nil {
		s.inflight = make(chan struct{})
		go s.refresh(s.inflight, s.etag, s.latest)
	}
	done := s.inflight
	s.mu.Unlock()
	// A disconnected browser stops waiting without cancelling other owners' check.
	select {
	case <-done:
	case <-ctx.Done():
	case <-s.ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

// snapshot runs under mu and returns copies so callers cannot mutate the cache.
func (s *Service) snapshot() Status {
	status := Status{State: "unknown"}
	if s.latest != nil {
		latest := *s.latest
		status.Latest = &latest
		checked := s.checkedAt
		status.CheckedAt = &checked
		status.Stale = s.failed || !s.now().Before(s.checkedAt.Add(12*time.Hour))
		status.State = compare(s.current.Version, latest.Version)
		if status.Stale && status.State == "current" {
			status.State = "unknown"
		}
	}
	if s.failed {
		retry := s.nextAttempt
		status.RetryAt = &retry
	}
	return status
}

func (s *Service) refresh(done chan struct{}, etag string, previous *LatestRelease) {
	ctx, cancel := context.WithTimeout(s.ctx, requestTimeout)
	defer cancel()
	root, _ := upstream.NewRoot("https://api.github.com/repos/themartz90/folderhug/releases/latest", false)
	headers := make(http.Header)
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("User-Agent", "Folderhug")
	headers.Set("X-GitHub-Api-Version", "2026-03-10")
	if etag != "" {
		headers.Set("If-None-Match", etag)
	}
	var latest *LatestRelease
	var res *http.Response
	var err error
	if s.client == nil {
		err = errors.New("update client unavailable")
	} else {
		res, err = s.client.Do(ctx, root, http.MethodGet, "", upstream.Options{Headers: headers, MaxBytes: maxBody, Timeout: requestTimeout})
	}
	newETag := etag
	if err == nil {
		if res == nil || res.Body == nil {
			err = errors.New("missing update response")
		} else {
			// The service enforces its own limit and body deadline, including injected clients.
			stop := context.AfterFunc(ctx, func() { res.Body.Close() })
			var body []byte
			body, err = io.ReadAll(io.LimitReader(res.Body, maxBody+1))
			stop()
			res.Body.Close()
			if err == nil && (len(body) > maxBody || ctx.Err() != nil) {
				err = errors.New("update response exceeded its budget")
			}
			if err == nil {
				switch res.StatusCode {
				case http.StatusOK:
					latest, err = parseRelease(body)
					newETag = safeETag(res.Header.Get("ETag"))
				case http.StatusNotModified:
					if previous == nil || etag == "" {
						err = errors.New("unexpected not-modified response")
					} else {
						latest = previous
					}
				default:
					err = errors.New("release unavailable")
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if err == nil {
		s.latest, s.checkedAt, s.etag, s.failed = latest, now, newETag, false
		s.nextAttempt = now.Add(12 * time.Hour)
	} else {
		s.failed = true
		s.nextAttempt = now.Add(time.Hour)
		if res != nil {
			if retry := retryDeadline(res, now); retry.After(s.nextAttempt) {
				s.nextAttempt = retry
			}
		}
	}
	s.inflight = nil
	close(done)
}

func parseRelease(body []byte) (*LatestRelease, error) {
	var payload struct {
		Tag         string `json:"tag_name"`
		URL         string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       *bool  `json:"draft"`
		Prerelease  *bool  `json:"prerelease"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("invalid release response")
	}
	version, ok := buildinfo.ParseVersion(payload.Tag)
	if !ok || version.IsPrerelease() || payload.Draft == nil || *payload.Draft || payload.Prerelease == nil || *payload.Prerelease || payload.URL != "https://github.com/themartz90/folderhug/releases/tag/"+payload.Tag {
		return nil, errors.New("invalid release identity")
	}
	if _, err := time.Parse(time.RFC3339, payload.PublishedAt); err != nil {
		return nil, errors.New("invalid release publication date")
	}
	return &LatestRelease{Version: payload.Tag, ReleaseURL: payload.URL, PublishedAt: payload.PublishedAt}, nil
}

func safeETag(raw string) string {
	if len(raw) > 1024 || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	return raw
}

func retryDeadline(res *http.Response, now time.Time) time.Time {
	var deadline time.Time
	raw := res.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		deadline = now.Add(time.Duration(seconds) * time.Second)
	} else if date, err := http.ParseTime(raw); err == nil {
		deadline = date
	}
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests || res.Header.Get("X-RateLimit-Remaining") == "0" {
		if seconds, err := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && seconds > 0 {
			reset := time.Unix(seconds, 0)
			if reset.Year() <= 9999 && reset.After(deadline) {
				deadline = reset
			}
		}
	}
	return deadline
}
