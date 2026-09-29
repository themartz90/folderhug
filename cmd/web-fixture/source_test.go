//go:build webfixture

package main

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	directorylisting "github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/upstream"
)

func TestSyntheticListingsAndImages(t *testing.T) {
	source := newSource()
	root, err := upstream.NewRoot("http://fixture.example.test/library/", true)
	if err != nil {
		t.Fatal(err)
	}
	for path, count := range map[string]int{"": 6, "Empty/": 0, "Large/": 10000} {
		current, err := root.Resolve(path)
		if err != nil {
			t.Fatal(err)
		}
		entries, _, err := directorylisting.Parse(bytes.NewReader(source.listings[current.Path]), root, current, "test", 1)
		if err != nil || len(entries) != count {
			t.Fatalf("%s: count=%d error=%v", path, len(entries), err)
		}
		if path == "Large/" {
			var unknown, unicode, long bool
			for _, entry := range entries {
				unknown = unknown || (entry.Size == nil && entry.ModifiedAt == nil)
				unicode = unicode || entry.Name == "café & snow 雪.txt"
				long = long || len(entry.Name) > 150
			}
			if !unknown || !unicode || !long {
				t.Fatal("large listing lacks required metadata/name cases")
			}
		}
	}
	for _, filename := range []string{"mountain.png", "sunset.jpg"} {
		w := httptest.NewRecorder()
		source.ServeHTTP(w, httptest.NewRequest("GET", "http://fixture.example.test/library/Media/"+filename, nil))
		img, _, err := image.Decode(w.Body)
		if w.Code != 200 || err != nil || img.Bounds().Dx() != 120 {
			t.Fatalf("invalid fixture image %s: %v", filename, err)
		}
	}
}

func TestSyntheticSlowDownloadHasRangesAndCancellation(t *testing.T) {
	server := httptest.NewServer(newSource())
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL+"/library/slow-download.bin", nil)
	request.Header.Set("Range", "bytes=10-29")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 206 || response.Header.Get("Content-Range") != "bytes 10-29/4194304" || len(body) != 20 || body[0] != 10 {
		t.Fatalf("invalid range response: %d %v", response.StatusCode, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request, _ = http.NewRequestWithContext(ctx, "GET", server.URL+"/library/slow-download.bin", nil)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err == nil || n == 0 || n >= 4<<20 {
		t.Fatalf("expected interrupted partial stream: bytes=%d error=%v", n, err)
	}
}
