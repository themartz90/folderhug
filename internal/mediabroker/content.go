package mediabroker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/themartz90/folderhug/internal/content"
	"github.com/themartz90/folderhug/internal/upstream"
)

func strongETag(value string) bool {
	if len(value) < 2 || len(value) > 1024 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, c := range value[1 : len(value)-1] {
		if c < 33 || c == '"' || c == 127 {
			return false
		}
	}
	return true
}

func decimal(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	return v, err == nil
}

func contentRange(value string) (start, end, total int64, ok bool) {
	if !strings.HasPrefix(value, "bytes ") {
		return
	}
	rangeText, totalText, found := strings.Cut(strings.TrimPrefix(value, "bytes "), "/")
	if !found {
		return
	}
	startText, endText, found := strings.Cut(rangeText, "-")
	if !found {
		return
	}
	start, a := decimal(startText)
	end, c := decimal(endText)
	total, d := decimal(totalText)
	return start, end, total, a && c && d && total > 0 && start <= end && end < total
}

func (b *Broker) probe(j *lease) error {
	r, err := b.fetcher.Do(j.ctx, j.root, "GET", j.path, upstream.Options{Credentials: j.credentials, Headers: http.Header{"Range": {"bytes=0-0"}}, MaxBytes: 1, Timeout: jobTimeout})
	if err != nil {
		return err
	}
	defer r.Body.Close()
	a, z, total, ok := contentRange(r.Header.Get("Content-Range"))
	if r.StatusCode != 206 || !ok || a != 0 || z != 0 || r.ContentLength != 1 || !identity(r) || !strongETag(r.Header.Get("ETag")) {
		return ErrUnavailable
	}
	var one [1]byte
	if _, err = io.ReadFull(r.Body, one[:]); err != nil {
		return ErrUnavailable
	}
	j.size = total
	j.etag = r.Header.Get("ETag")
	j.consumed = 1
	return nil
}

func identity(r *http.Response) bool {
	v := r.Header.Get("Content-Encoding")
	return v == "" || strings.EqualFold(v, "identity")
}

func boundedRange(raw string, size, budget int64) (start, end int64, ok bool) {
	if size < 1 || budget < 1 {
		return
	}
	if raw == "" {
		raw = "bytes=0-"
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.ContainsAny(raw, ", \t\r\n") {
		return
	}
	a, z, found := strings.Cut(strings.TrimPrefix(raw, "bytes="), "-")
	if !found {
		return
	}
	if a == "" {
		suffix, valid := decimal(z)
		if !valid || suffix == 0 {
			return
		}
		start = max(0, size-suffix)
		end = size - 1
	} else {
		var valid bool
		start, valid = decimal(a)
		if !valid || start >= size {
			return
		}
		end = size - 1
		if z != "" {
			end, valid = decimal(z)
			if !valid || end < start {
				return
			}
			end = min(end, size-1)
		}
	}
	end = start + min(end-start, budget-1)
	return start, end, true
}

func (b *Broker) file(w http.ResponseWriter, r *http.Request) {
	j := b.current(r)
	if j == nil {
		w.WriteHeader(401)
		return
	}
	if len(r.Header.Values("Range")) > 1 || len(r.Header.Values("If-Range")) > 1 {
		w.WriteHeader(400)
		return
	}
	if validator := r.Header.Get("If-Range"); validator != "" && validator != j.etag {
		w.WriteHeader(412)
		return
	}
	select {
	case j.transfer <- struct{}{}:
		defer func() { <-j.transfer }()
	case <-j.ctx.Done():
		w.WriteHeader(410)
		return
	case <-r.Context().Done():
		return
	}
	if b.current(r) != j {
		w.WriteHeader(401)
		return
	}
	remaining := byteBudget - j.consumed
	a, z, ok := boundedRange(r.Header.Get("Range"), j.size, remaining)
	if !ok {
		if remaining < 1 {
			j.cancel()
			w.WriteHeader(413)
		} else {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", j.size))
			w.WriteHeader(416)
		}
		return
	}
	if r.Method == "HEAD" {
		w.Header().Set("Content-Length", strconv.FormatInt(z-a+1, 10))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, z, j.size))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", j.etag)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(206)
		return
	}
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	res, err := b.fetcher.Do(ctx, j.root, "GET", j.path, upstream.Options{Credentials: j.credentials, Headers: http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", a, z)}, "If-Match": {j.etag}}, MaxBytes: remaining, Timeout: jobTimeout})
	if err != nil {
		j.cancel()
		w.WriteHeader(502)
		return
	}
	defer res.Body.Close()
	start, end, total, valid := contentRange(res.Header.Get("Content-Range"))
	if res.StatusCode != 206 || !valid || start != a || end != z || total != j.size || res.ContentLength != z-a+1 || res.Header.Get("ETag") != j.etag || !identity(res) {
		j.cancel()
		w.WriteHeader(502)
		return
	}
	writer, finish, err := content.ResponseWriter(ctx, w)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	defer finish()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(z-a+1, 10))
	w.Header().Set("Content-Range", res.Header.Get("Content-Range"))
	w.Header().Set("ETag", j.etag)
	w.WriteHeader(206)
	buffer := make([]byte, 32<<10)
	left := z - a + 1
	for left > 0 {
		n, readErr := res.Body.Read(buffer[:min(int64(len(buffer)), left)])
		j.consumed += int64(n)
		left -= int64(n)
		if n > 0 {
			written, writeErr := writer.Write(buffer[:n])
			if writeErr != nil || written != n {
				panic(http.ErrAbortHandler)
			}
		}
		if readErr != nil && !(readErr == io.EOF && left == 0) {
			j.cancel()
			panic(http.ErrAbortHandler)
		}
		if n == 0 && readErr == nil {
			j.cancel()
			panic(http.ErrAbortHandler)
		}
	}
}
