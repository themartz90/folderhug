// Package testfixture serves synthetic upstream listings and file responses.
package testfixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Format string

const (
	ApacheTable Format = "apache-table"
	ApachePre   Format = "apache-pre"
	Nginx       Format = "nginx"
)

type Behavior string

const (
	Normal         Behavior = "normal"
	IgnoredRange   Behavior = "ignored-range"
	MalformedRange Behavior = "malformed-range"
	ChangedETag    Behavior = "changed-etag"
	Throttled      Behavior = "throttled"
	Slow           Behavior = "slow"
	Interrupted    Behavior = "interrupted"
	MissingHEAD    Behavior = "missing-head"
	Redirect       Behavior = "redirect"
)

var modified = time.Date(2026, 1, 2, 12, 30, 0, 0, time.UTC)

// File uses Data when non-nil, otherwise Size bytes are generated as they are read.
type File struct {
	Data        []byte
	Size        int64
	ContentType string
}

func DeterministicFile(size int64) File {
	if size < 0 {
		panic("negative synthetic file size")
	}
	return File{Size: size, ContentType: "application/octet-stream"}
}

// Config is copied by Handler. Files use decoded, absolute URL paths.
// Query parameters format and behavior override the configured defaults.
type Config struct {
	Format         Format
	Behavior       Behavior
	Files          map[string]File
	Username       string
	Password       string
	ChunkDelay     time.Duration
	RetryAfter     time.Duration
	RedirectTo     string
	InterruptAfter int64
}

func DefaultFiles() map[string]File {
	return map[string]File{
		"/sample.bin":                    DeterministicFile(64 * 1024),
		"/empty.txt":                     {Data: []byte{}, ContentType: "text/plain"},
		"/nested/hello world.txt":        {Data: []byte("Synthetic file.\n"), ContentType: "text/plain"},
		"/nested/deeper/100% #what?.txt": {Data: []byte("Reserved URL characters.\n"), ContentType: "text/plain"},
		"/nested/café & snow 雪.txt":      {Data: []byte("Synthetic Unicode filename.\n"), ContentType: "text/plain"},
	}
}

// Handler never reads local files or fetches other servers. RedirectTo is only
// emitted as a Location header, allowing tests to supply a second local server.
func Handler(config Config) http.Handler {
	if config.Format == "" {
		config.Format = ApacheTable
	}
	if config.Behavior == "" {
		config.Behavior = Normal
	}
	if config.ChunkDelay <= 0 {
		config.ChunkDelay = 25 * time.Millisecond
	}
	if config.RetryAfter <= 0 {
		config.RetryAfter = 2 * time.Second
	}
	if config.InterruptAfter <= 0 {
		config.InterruptAfter = 1024
	}
	if config.RedirectTo == "" {
		config.RedirectTo = "/"
	}
	if config.Files == nil {
		config.Files = DefaultFiles()
	}
	files := make(map[string]File, len(config.Files))
	for name, file := range config.Files {
		if !strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || path.Clean(name) != name || file.Size < 0 {
			panic("invalid synthetic file configuration")
		}
		if file.Data != nil {
			file.Data = bytes.Clone(file.Data)
			file.Size = int64(len(file.Data))
		}
		files[name] = file
	}
	var revision atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if config.Username != "" || config.Password != "" {
			user, password, ok := r.BasicAuth()
			if !ok || user != config.Username || password != config.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="synthetic-fixture"`)
				http.Error(w, "Fixture credentials required", http.StatusUnauthorized)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		behavior := config.Behavior
		filePath := r.URL.Path
		if strings.HasPrefix(filePath, "/_fixture/") {
			parts := strings.SplitN(strings.TrimPrefix(filePath, "/_fixture/"), "/", 2)
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			behavior, filePath = Behavior(parts[0]), "/"+parts[1]
		}
		if value := r.URL.Query().Get("behavior"); value != "" {
			behavior = Behavior(value)
		}
		if !validBehavior(behavior) {
			http.Error(w, "Unknown fixture behavior", http.StatusBadRequest)
			return
		}
		if behavior == Throttled {
			w.Header().Set("Retry-After", strconv.FormatInt(int64((config.RetryAfter+time.Second-1)/time.Second), 10))
			http.Error(w, "Synthetic rate limit", http.StatusTooManyRequests)
			return
		}
		if behavior == Redirect {
			http.Redirect(w, r, config.RedirectTo, http.StatusFound)
			return
		}
		if behavior == MissingHEAD && r.Method == http.MethodHead {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		file, exists := files[filePath]
		if !exists {
			if behavior == Slow {
				timer := time.NewTimer(config.ChunkDelay)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
					return
				case <-timer.C:
				}
			}
			serveDirectory(w, r, filePath, config.Format, files)
			return
		}
		salt := uint64(0)
		if behavior == ChangedETag {
			salt = revision.Add(1)
		}
		var reader io.ReadSeeker = &patternReader{size: file.Size, salt: salt}
		etag := fmt.Sprintf(`"synthetic-%d-%d"`, file.Size, salt)
		if file.Data != nil {
			reader = bytes.NewReader(file.Data)
			etag = fmt.Sprintf(`"%x-%d"`, sha256.Sum256(file.Data), salt)
		}
		w.Header().Set("ETag", etag)
		contentType := file.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		if behavior == IgnoredRange {
			r = r.Clone(r.Context())
			r.Header.Del("Range")
			r.Header.Del("If-Range")
		}
		if behavior == MalformedRange && r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes invalid")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusPartialContent)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "bad!")
			}
			return
		}
		if behavior == Slow {
			reader = &slowReader{ReadSeeker: reader, ctx: r.Context(), delay: config.ChunkDelay}
		}
		if behavior == Interrupted && r.Method == http.MethodGet {
			w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
			w.WriteHeader(http.StatusOK)
			n := min(config.InterruptAfter, max(file.Size-1, 0))
			_, _ = io.CopyN(w, reader, n)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Abort after flushing a prefix so clients observe a truncated body.
			panic(http.ErrAbortHandler)
		}
		http.ServeContent(w, r, path.Base(filePath), modified, reader)
	})
}

func validBehavior(b Behavior) bool {
	switch b {
	case Normal, IgnoredRange, MalformedRange, ChangedETag, Throttled, Slow, Interrupted, MissingHEAD, Redirect:
		return true
	}
	return false
}

type patternReader struct {
	size, offset int64
	salt         uint64
}

func (r *patternReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.size-r.offset))
	for i := range p[:n] {
		p[i] = byte((uint64(r.offset) + uint64(i) + r.salt) % 251)
	}
	r.offset += int64(n)
	return n, nil
}

func (r *patternReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.offset
	case io.SeekEnd:
		offset += r.size
	default:
		return 0, fmt.Errorf("invalid seek origin")
	}
	if offset < 0 {
		return 0, fmt.Errorf("negative seek position")
	}
	r.offset = offset
	return offset, nil
}

type slowReader struct {
	io.ReadSeeker
	ctx   context.Context
	delay time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-timer.C:
	}
	return r.ReadSeeker.Read(p[:min(len(p), 4096)])
}

func serveDirectory(w http.ResponseWriter, r *http.Request, dir string, format Format, files map[string]File) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	entries := make(map[string]listingEntry)
	for filename, file := range files {
		if !strings.HasPrefix(filename, prefix) {
			continue
		}
		relative := strings.TrimPrefix(filename, prefix)
		name, _, directory := strings.Cut(relative, "/")
		if directory {
			name += "/"
		}
		entries[name] = listingEntry{Name: name, Href: url.PathEscape(strings.TrimSuffix(name, "/")), Size: file.Size, Directory: directory}
	}
	if len(entries) == 0 {
		http.NotFound(w, r)
		return
	}
	if !strings.HasSuffix(dir, "/") {
		target := *r.URL
		target.Path += "/"
		target.RawPath = ""
		http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
		return
	}
	if value := r.URL.Query().Get("format"); value != "" {
		format = Format(value)
	}
	if format != ApacheTable && format != ApachePre && format != Nginx {
		http.Error(w, "Unknown fixture format", http.StatusBadRequest)
		return
	}
	ordered := make([]listingEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Directory {
			entry.Href += "/"
		}
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	body := renderListing(format, dir, ordered)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", modified, bytes.NewReader(body))
}
