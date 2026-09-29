//go:build webfixture

package main

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/themartz90/folderhug/internal/testfixture"
)

type source struct {
	offline  atomic.Bool
	delay    atomic.Int64
	files    http.Handler
	slow     http.Handler
	listings map[string][]byte
}

func newSource() *source {
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 3), B: 110, A: 255})
		}
	}
	var pngData, jpegData bytes.Buffer
	_ = png.Encode(&pngData, img)
	_ = jpeg.Encode(&jpegData, img, &jpeg.Options{Quality: 80})
	files := map[string]testfixture.File{
		"/library/readme.txt":                  {Data: []byte("Synthetic HTTP Wrapper browser fixture.\n"), ContentType: "text/plain"},
		"/library/Media/mountain.png":          {Data: pngData.Bytes(), ContentType: "image/png"},
		"/library/Media/sunset.jpg":            {Data: jpegData.Bytes(), ContentType: "image/jpeg"},
		"/library/Media/unsupported-video.mp4": {Data: []byte("synthetic unsupported video bytes"), ContentType: "video/mp4"},
		"/library/Scan/notes.txt":              {Data: []byte("Scan fixture\n"), ContentType: "text/plain"},
		"/library/Scan/Nested/report.txt":      {Data: []byte("Nested scan fixture\n"), ContentType: "text/plain"},
		"/library/slow-download.bin":           testfixture.DeterministicFile(4 << 20),
	}
	large := make([]row, 10000)
	extensions := []string{"txt", "png", "mp4", "mp3", "zip", "bin"}
	for i := range large {
		name := fmt.Sprintf("file-%05d.%s", i+1, extensions[i%len(extensions)])
		if i == 4 {
			name = "café & snow 雪.txt"
		}
		if i == 5 {
			name = strings.Repeat("long filename ", 12) + "report.txt"
		}
		if i == 6 {
			name = "100% #what?.txt"
		}
		large[i] = row{name: name, size: fmt.Sprint(i + 10), modified: "2026-01-02 12:30"}
		if i%11 == 0 {
			large[i].size = "-"
			large[i].modified = ""
		}
		files["/library/Large/"+name] = testfixture.DeterministicFile(int64(i + 10))
	}
	listings := map[string][]byte{
		"/library/":       listing("/library/", []row{{name: "Media/"}, {name: "Empty/"}, {name: "Large/"}, {name: "Scan/"}, {name: "readme.txt", size: "39"}, {name: "slow-download.bin", size: "4194304"}}),
		"/library/Empty/": listing("/library/Empty/", nil),
		"/library/Large/": listing("/library/Large/", large),
	}
	return &source{files: testfixture.Handler(testfixture.Config{Files: files}), slow: testfixture.Handler(testfixture.Config{Files: files, Behavior: testfixture.Slow, ChunkDelay: 12 * time.Millisecond}), listings: listings}
}

type row struct{ name, size, modified string }

func listing(directory string, rows []row) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "<!doctype html><html><head><title>Index of %s</title></head><body><h1>Index of %s</h1><table><tr><th>Name</th><th>Last modified</th><th>Size</th></tr><tr><td><a href=\"../\">Parent Directory</a></td><td></td><td>-</td></tr>", html.EscapeString(directory), html.EscapeString(directory))
	for _, r := range rows {
		href := url.PathEscape(strings.TrimSuffix(r.name, "/"))
		if strings.HasSuffix(r.name, "/") {
			href += "/"
		}
		fmt.Fprintf(&out, "<tr><td><a href=\"%s\">%s</a></td><td>%s</td><td>%s</td></tr>", html.EscapeString(href), html.EscapeString(r.name), r.modified, r.size)
	}
	out.WriteString("</table></body></html>")
	return out.Bytes()
}

func (s *source) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.offline.Load() || strings.HasPrefix(r.URL.Path, "/offline/") {
		http.Error(w, "Synthetic source offline", 503)
		return
	}
	if delay := s.delay.Load(); delay > 0 {
		timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
	}
	if strings.HasPrefix(r.URL.Path, "/unsupported/") {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!doctype html><html><body><p>Synthetic page without a directory listing.</p></body></html>")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/locked/") {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "fixture-user" || pass != "fixture-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="synthetic-fixture"`)
			http.Error(w, "Fixture credentials required", 401)
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path = strings.Replace(r.URL.Path, "/locked/", "/library/", 1)
		r.URL.RawPath = ""
	}
	if body, ok := s.listings[r.URL.Path]; ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Date(2026, 1, 2, 12, 30, 0, 0, time.UTC), bytes.NewReader(body))
		return
	}
	if r.URL.Path == "/library/slow-download.bin" {
		s.slow.ServeHTTP(w, r)
		return
	}
	s.files.ServeHTTP(w, r)
}
