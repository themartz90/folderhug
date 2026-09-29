package content

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func passiveMIME(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "audio/mpeg", "audio/ogg", "audio/wav", "audio/webm", "audio/mp4", "video/mp4", "video/webm", "video/ogg":
		return true
	}
	return false
}

func sniffMatches(declared string, prefix []byte) bool {
	if !passiveMIME(declared) {
		return false
	}
	detected := normalizedMIME(http.DetectContentType(prefix))
	if detected == declared {
		return true
	}
	switch detected {
	case "application/ogg":
		return declared == "audio/ogg" || declared == "video/ogg"
	case "video/mp4":
		return declared == "audio/mp4"
	case "video/webm":
		return declared == "audio/webm"
	}
	return false
}

func readPrefix(res *http.Response) ([]byte, bool, error) {
	size := int64(512)
	if res.ContentLength >= 0 {
		size = min(size, res.ContentLength)
	}
	buf := make([]byte, size)
	used := 0
	for used < len(buf) {
		n, err := res.Body.Read(buf[used:])
		used += n
		if err != nil {
			if err == io.EOF && (res.ContentLength < 0 || int64(used) == res.ContentLength) {
				return buf[:used], true, nil
			}
			return nil, false, transportError(err)
		}
		if n == 0 {
			return nil, false, failure(502, "upstream_unavailable", "The source stopped sending file data.")
		}
	}
	return buf, res.ContentLength >= 0 && int64(used) == res.ContentLength, nil
}

// sniffProbe closes its response before the actual range request. Holding both
// responses would allow concurrent previews to exhaust the same origin's slots.
func (s *Service) sniffProbe(ctx context.Context, src Source) (string, error) {
	headers := http.Header{"Range": {"bytes=0-511"}}
	if src.ExpectedETag != "" {
		headers.Set("If-Match", src.ExpectedETag)
	}
	res, err := s.fetch(ctx, src, "GET", headers, true)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return "", responseError(res)
	}
	if err := verifyHeaders(res, &byteRange{start: 0, end: 511}, src.ExpectedETag); err != nil {
		return "", err
	}
	prefix, _, err := readPrefix(res)
	if err != nil {
		return "", err
	}
	if !sniffMatches(normalizedMIME(res.Header.Get("Content-Type")), prefix) {
		return "", failure(415, "unsupported_media", "This file cannot be previewed safely.")
	}
	meta := metadata(res)
	if !meta.StrongETag {
		return "", failure(415, "unsupported_media", "This source cannot verify ranged previews. Download the file instead.")
	}
	return meta.ETag, nil
}

func requestHeaders(r *http.Request) http.Header {
	headers := make(http.Header)
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		if value := r.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	return headers
}

// Serve commits headers only after validating the response. It returns errors
// only before commitment. A failure after writing starts aborts the HTTP stream.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, src Source, preview bool) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return failure(405, "invalid_request", "Only GET and HEAD are supported.")
	}
	if len(r.Header.Values("Range")) > 1 {
		return failure(400, "invalid_request", "Only one byte range is supported.")
	}
	requested, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.limits.Timeout)
	defer cancel()
	headers := requestHeaders(r)
	expected := src.ExpectedETag
	if preview && r.Method == http.MethodGet && requested != nil {
		expected, err = s.sniffProbe(ctx, src)
		if err != nil {
			return err
		}
	}
	if expected != "" {
		headers.Set("If-Match", expected)
	}
	res, err := s.fetch(ctx, src, r.Method, headers, r.Method == http.MethodHead)
	if err != nil {
		return err
	}
	if r.Method == http.MethodHead && (res.StatusCode == 405 || res.StatusCode == 501) {
		res.Body.Close()
		res, err = s.headFallback(ctx, src, headers)
		if err != nil {
			return err
		}
		requested = nil
	}
	defer res.Body.Close()
	stop := context.AfterFunc(ctx, func() { res.Body.Close() })
	defer stop()
	if res.StatusCode == 416 {
		if err := verifyRepresentation(res, expected); err != nil {
			return err
		}
		return rangeError(res, requested)
	}
	if res.StatusCode == 304 {
		if headers.Get("If-None-Match") == "" && headers.Get("If-Modified-Since") == "" {
			return failure(502, "upstream_unavailable", "The source sent an unexpected conditional response.")
		}
		if expected != "" && res.Header.Get("ETag") != "" && res.Header.Get("ETag") != expected {
			return failure(412, "representation_changed", "The source file changed. Start the request again.")
		}
		if etag := validETag(res.Header.Get("ETag")); etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(304)
		return nil
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return responseError(res)
	}
	if err := verifyHeaders(res, requested, expected); err != nil {
		return err
	}
	if res.StatusCode == 206 {
		if value := headers.Get("If-Range"); value != "" {
			if validETag(value) != "" {
				if strings.HasPrefix(value, "W/") || res.Header.Get("ETag") != value {
					return failure(412, "representation_changed", "The source file changed. Start the request again.")
				}
			} else {
				requestedDate, requestErr := http.ParseTime(value)
				modified, sourceErr := http.ParseTime(res.Header.Get("Last-Modified"))
				if requestErr != nil || sourceErr != nil || !requestedDate.Equal(modified) {
					return failure(412, "representation_changed", "The source file changed. Start the request again.")
				}
			}
		}
	}
	if res.ContentLength > s.limits.MaxBytes && r.Method != http.MethodHead {
		return failure(413, "budget_exceeded", "The file exceeds the transfer limit.")
	}
	var prefix []byte
	var complete bool
	if preview {
		if !passiveMIME(normalizedMIME(res.Header.Get("Content-Type"))) {
			return failure(415, "unsupported_media", "This file cannot be previewed safely.")
		}
		if r.Method == http.MethodGet && (requested == nil || res.StatusCode == 200) {
			prefix, complete, err = readPrefix(res)
			if err != nil {
				return err
			}
			if !sniffMatches(normalizedMIME(res.Header.Get("Content-Type")), prefix) {
				return failure(415, "unsupported_media", "This file cannot be previewed safely.")
			}
		}
	}
	writer, err := newDeadlineWriter(ctx, w)
	if err != nil {
		return failure(503, "unavailable", "The download response could not be started.")
	}
	defer writer.finish()
	setResponseHeaders(w, res, src, preview)
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	var reader io.Reader = bytes.NewReader(prefix)
	if !complete {
		reader = io.MultiReader(reader, res.Body)
	}
	if err := copyStream(ctx, writer, reader, res.ContentLength, s.limits.MaxBytes); err != nil {
		cancel()
		panic(http.ErrAbortHandler)
	}
	return nil
}

func (s *Service) headFallback(ctx context.Context, src Source, headers http.Header) (*http.Response, error) {
	headers = headers.Clone()
	headers.Set("Range", "bytes=0-0")
	headers.Del("If-Range")
	res, err := s.fetch(ctx, src, "GET", headers, true)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == 304 {
		return res, nil
	}
	if res.StatusCode == 416 && res.Header.Get("Content-Range") == "bytes */0" {
		if err := verifyRepresentation(res, src.ExpectedETag); err != nil {
			res.Body.Close()
			return nil, err
		}
		res.StatusCode, res.ContentLength = 200, 0
		res.Header.Del("Content-Range")
		res.Header.Set("Content-Length", "0")
		return res, nil
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		res.Body.Close()
		return nil, responseError(res)
	}
	if err := verifyHeaders(res, &byteRange{start: 0, end: 0}, src.ExpectedETag); err != nil {
		res.Body.Close()
		return nil, err
	}
	if res.StatusCode == 206 {
		c, _ := parseContentRange(res.Header.Get("Content-Range"))
		res.ContentLength, res.StatusCode = c.total, 200
		res.Header.Del("Content-Range")
		res.Header.Set("Content-Length", strconv.FormatInt(c.total, 10))
	}
	return res, nil
}

func copyStream(ctx context.Context, dst io.Writer, src io.Reader, expected, cap int64) error {
	buffer := make([]byte, 32<<10)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if int64(n) > cap-copied || (expected >= 0 && int64(n) > expected-copied) {
				return io.ErrUnexpectedEOF
			}
			written, err := dst.Write(buffer[:n])
			copied += int64(written)
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			if expected >= 0 && copied != expected {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}
