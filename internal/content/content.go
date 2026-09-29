// Package content validates upstream file responses and streams safe downloads.
package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/themartz90/folderhug/internal/upstream"
)

type Fetcher interface {
	Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)
}

type Source struct {
	Root         *upstream.Root
	Path         string
	Filename     string
	ExpectedETag string
	Credentials  *upstream.Credentials
}

// Limits defaults to a 1 TiB browser response, 24 hours per stream, and 30
// seconds per metadata or sniff probe. Each copy uses a 32 KiB buffer and a
// 30-second browser write deadline, also bounded by the stream timeout.
type Limits struct {
	MaxBytes              int64
	Timeout, ProbeTimeout time.Duration
}

type Service struct {
	fetcher Fetcher
	limits  Limits
}

type Metadata struct {
	Size        *int64
	SizeExact   bool
	ETag        string
	StrongETag  bool
	ModifiedAt  *time.Time
	ContentType string
	// Ranges is unknown, advertised, verified, or none.
	Ranges string
}

// Error contains only local messages and allowlisted response headers. Serve
// returns it before committing headers, for the API's structured error writer.
type Error struct {
	Status        int
	Code, Message string
	Headers       http.Header
}

func (e *Error) Error() string { return e.Message }
func failure(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func New(fetcher Fetcher, limits Limits) *Service {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 1 << 40
	}
	if limits.Timeout <= 0 {
		limits.Timeout = 24 * time.Hour
	}
	if limits.ProbeTimeout <= 0 {
		limits.ProbeTimeout = 30 * time.Second
	}
	return &Service{fetcher: fetcher, limits: limits}
}

func (s *Service) fetch(ctx context.Context, src Source, method string, headers http.Header, probe bool) (*http.Response, error) {
	timeout := s.limits.Timeout
	if probe {
		timeout = s.limits.ProbeTimeout
	}
	res, err := s.fetcher.Do(ctx, src.Root, method, src.Path, upstream.Options{Credentials: src.Credentials, Headers: headers, MaxBytes: s.limits.MaxBytes, Timeout: timeout, Interactive: method == "HEAD"})
	if err != nil {
		return nil, transportError(err)
	}
	if res == nil || res.Body == nil {
		return nil, failure(502, "upstream_unavailable", "The source returned an invalid response.")
	}
	return res, nil
}

func transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, upstream.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return failure(504, "upstream_timeout", "The source request timed out.")
	case errors.Is(err, upstream.ErrResponseLimit):
		return failure(413, "budget_exceeded", "The file exceeds the transfer limit.")
	case errors.Is(err, upstream.ErrURL):
		return failure(400, "unsafe_path", "The source path is not allowed.")
	case errors.Is(err, upstream.ErrPolicy):
		return failure(502, "unsafe_origin", "The source destination is not allowed.")
	default:
		return failure(502, "upstream_unavailable", "The source request failed.")
	}
}

func responseError(res *http.Response) error {
	switch res.StatusCode {
	case 401, 403:
		return failure(502, "upstream_auth_required", "The source credentials need attention.")
	case 404, 410:
		return failure(404, "not_found", "The source file is no longer available.")
	case 412:
		return failure(412, "representation_changed", "The source file changed. Start the request again.")
	case 429, 503:
		err := failure(503, "upstream_unavailable", "The source is temporarily unavailable.")
		if retry := safeRetryAfter(res.Header.Get("Retry-After")); retry != "" {
			err.Headers = http.Header{"Retry-After": {retry}}
		}
		return err
	default:
		return failure(502, "upstream_unavailable", "The source returned an unexpected response.")
	}
}

func safeRetryAfter(value string) string {
	if n, ok := decimal(value); ok && n <= 86400 {
		return strconv.FormatInt(n, 10)
	}
	if date, err := http.ParseTime(value); err == nil {
		return date.UTC().Format(http.TimeFormat)
	}
	return ""
}

func metadata(res *http.Response) Metadata {
	result := Metadata{ETag: validETag(res.Header.Get("ETag")), ContentType: normalizedMIME(res.Header.Get("Content-Type")), Ranges: "unknown"}
	result.StrongETag = result.ETag != "" && !strings.HasPrefix(result.ETag, "W/")
	if res.ContentLength >= 0 {
		size := res.ContentLength
		result.Size = &size
		result.SizeExact = true
	}
	if modified, err := http.ParseTime(res.Header.Get("Last-Modified")); err == nil {
		result.ModifiedAt = &modified
	}
	switch strings.ToLower(res.Header.Get("Accept-Ranges")) {
	case "bytes":
		result.Ranges = "advertised"
	case "none":
		result.Ranges = "none"
	}
	return result
}

func validETag(value string) string {
	raw := strings.TrimPrefix(value, "W/")
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return ""
	}
	for _, ch := range raw[1 : len(raw)-1] {
		if ch < 0x21 || ch == '"' || ch == 0x7f {
			return ""
		}
	}
	return value
}

func normalizedMIME(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream"
	}
	switch mediaType {
	case "image/jpg":
		return "image/jpeg"
	case "audio/x-wav", "audio/wave":
		return "audio/wav"
	}
	return mediaType
}

func verifyRepresentation(res *http.Response, expected string) error {
	if encoding := strings.TrimSpace(res.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return failure(502, "upstream_unavailable", "The source sent an encoded representation.")
	}
	if expected != "" && (validETag(expected) != expected || res.Header.Get("ETag") != expected) {
		return failure(412, "representation_changed", "The source file changed. Start the request again.")
	}
	return nil
}

func verifyHeaders(res *http.Response, requested *byteRange, expected string) error {
	if err := verifyRepresentation(res, expected); err != nil {
		return err
	}
	if text := res.Header.Get("Content-Length"); text != "" {
		n, ok := decimal(text)
		if !ok || n != res.ContentLength {
			return failure(502, "upstream_unavailable", "The source sent an inconsistent length.")
		}
	}
	if res.StatusCode == 206 {
		c, ok := parseContentRange(res.Header.Get("Content-Range"))
		if !ok || !requested.matches(c) || res.ContentLength != c.end-c.start+1 {
			return failure(502, "upstream_unavailable", "The source sent an invalid byte range.")
		}
	} else if res.Header.Get("Content-Range") != "" {
		return failure(502, "upstream_unavailable", "The source sent an unexpected byte range.")
	}
	return nil
}

// Metadata uses HEAD first. A server without HEAD gets one range request; an
// ignored range is closed immediately without consuming its full response.
func (s *Service) Metadata(ctx context.Context, src Source) (Metadata, error) {
	res, err := s.fetch(ctx, src, "HEAD", nil, true)
	if err != nil {
		return Metadata{}, err
	}
	if res.StatusCode != 405 && res.StatusCode != 501 {
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return Metadata{}, responseError(res)
		}
		if err := verifyHeaders(res, nil, src.ExpectedETag); err != nil {
			return Metadata{}, err
		}
		return metadata(res), nil
	}
	res.Body.Close()
	res, err = s.fetch(ctx, src, "GET", http.Header{"Range": {"bytes=0-0"}}, true)
	if err != nil {
		return Metadata{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == 416 && res.Header.Get("Content-Range") == "bytes */0" {
		if err := verifyRepresentation(res, src.ExpectedETag); err != nil {
			return Metadata{}, err
		}
		zero := int64(0)
		result := metadata(res)
		result.Size, result.SizeExact, result.Ranges = &zero, true, "verified"
		return result, nil
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return Metadata{}, responseError(res)
	}
	if err := verifyHeaders(res, &byteRange{start: 0, end: 0}, src.ExpectedETag); err != nil {
		return Metadata{}, err
	}
	result := metadata(res)
	if res.StatusCode == 206 {
		c, _ := parseContentRange(res.Header.Get("Content-Range"))
		result.Size, result.SizeExact, result.Ranges = &c.total, true, "verified"
		var one [1]byte
		if _, err := io.ReadFull(res.Body, one[:]); err != nil {
			return Metadata{}, transportError(err)
		}
	} else {
		result.Ranges = "none"
	}
	return result, nil
}

func safeFilename(src Source) string {
	name := src.Filename
	if name == "" {
		name, _ = url.PathUnescape(path.Base(src.Path))
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) || unicode.In(ch, unicode.Cf) {
			return -1
		}
		return ch
	}, name)
	name = strings.Trim(name, " .")
	if name == "" || name == "/" {
		return "download"
	}
	return name
}

func setResponseHeaders(w http.ResponseWriter, res *http.Response, src Source, preview bool) {
	h := w.Header()
	mediaType := normalizedMIME(res.Header.Get("Content-Type"))
	if !passiveMIME(mediaType) {
		mediaType = "application/octet-stream"
	}
	h.Set("Content-Type", mediaType)
	disposition := "attachment"
	if preview {
		disposition = "inline"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": safeFilename(src)}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	if etag := validETag(res.Header.Get("ETag")); etag != "" {
		h.Set("ETag", etag)
	}
	if modified, err := http.ParseTime(res.Header.Get("Last-Modified")); err == nil {
		h.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	}
	if value := strings.ToLower(res.Header.Get("Accept-Ranges")); value == "bytes" || value == "none" {
		h.Set("Accept-Ranges", value)
	}
	if res.ContentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(res.ContentLength, 10))
	}
	if res.StatusCode == 206 {
		h.Set("Content-Range", res.Header.Get("Content-Range"))
	}
}

func rangeError(res *http.Response, requested *byteRange) error {
	value := res.Header.Get("Content-Range")
	total, ok := decimal(strings.TrimPrefix(value, "bytes */"))
	if !strings.HasPrefix(value, "bytes */") || !ok || requested == nil || (total > 0 && (requested.suffix > 0 || requested.start < total)) {
		return failure(502, "upstream_unavailable", "The source rejected a valid byte range.")
	}
	err := failure(416, "range_not_satisfiable", "The byte range is outside the file.")
	err.Headers = http.Header{"Content-Range": {fmt.Sprintf("bytes */%d", total)}}
	return err
}
