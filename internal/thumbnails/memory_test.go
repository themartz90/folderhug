package thumbnails

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/testfixture"
)

// This procedural image keeps fixture generation from retaining a decoded frame.
type memoryFixtureImage struct{}

func (memoryFixtureImage) ColorModel() color.Model { return color.NRGBAModel }
func (memoryFixtureImage) Bounds() image.Rectangle { return image.Rect(0, 0, 4000, 4000) }
func (memoryFixtureImage) At(x, y int) color.Color {
	return color.NRGBA{R: uint8(x / 8), G: uint8(y / 8), B: uint8((x + y) / 16), A: 192}
}

func TestThumbnailConcurrentMaximumImageMemory(t *testing.T) {
	if os.Getenv("FOLDERHUG_THUMBNAIL_BENCHMARK") != "1" && os.Getenv("HTTPWRAPPER_THUMBNAIL_BENCHMARK") != "1" {
		t.Skip("set FOLDERHUG_THUMBNAIL_BENCHMARK=1 for the two-worker memory acceptance test")
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&encoded, memoryFixtureImage{}); err != nil {
		t.Fatal(err)
	}
	payload := encoded.Bytes()
	if len(payload) > maxInput || len(payload) < 26 || payload[24] != 8 || payload[25] != 6 {
		t.Fatalf("fixture is not an accepted 8-bit RGBA PNG: %d bytes", len(payload))
	}
	f := newThumbnailFixture(t)
	gate := make(chan struct{})
	f.source.mu.Lock()
	f.source.files = map[string]testfixture.File{
		"/root/first.png":  {Data: payload, ContentType: "image/png"},
		"/root/second.png": {Data: payload, ContentType: "image/png"},
	}
	f.source.gate = gate
	f.source.mu.Unlock()
	page, err := f.catalog.Browse(context.Background(), f.profile.ID, profiles.LeaseID(f.p), "root", catalog.Query{Refresh: true})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("fixture listing: %d entries, %v", len(page.Entries), err)
	}
	ids := []string{page.Entries[0].ID, page.Entries[1].ID}
	runtime.GC()
	var before, after, retained runtime.MemStats
	runtime.ReadMemStats(&before)
	peakHeap := before.HeapAlloc
	stopSampling := make(chan struct{})
	samplingDone := make(chan struct{})
	var finishOnce sync.Once
	finishSampling := func() { finishOnce.Do(func() { close(stopSampling); <-samplingDone }) }
	defer finishSampling()
	go func() {
		defer close(samplingDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSampling:
				return
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				peakHeap = max(peakHeap, current.HeapAlloc)
			}
		}
	}()
	started := time.Now()
	items, err := f.s.Request(context.Background(), f.p, f.profile.ID, ids)
	if err != nil || len(items) != 2 {
		t.Fatalf("thumbnail requests: %v, %v", items, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	concurrent := false
	for time.Now().Before(deadline) {
		f.source.mu.Lock()
		concurrent = f.source.active == 2
		f.source.mu.Unlock()
		if concurrent {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	if !concurrent {
		t.Fatal("the two thumbnail workers did not reach the source concurrently")
	}
	completed := map[string]bool{}
	var outputBytes int64
	deadline = time.Now().Add(40 * time.Second)
	for len(completed) < 2 && time.Now().Before(deadline) {
		for _, pending := range items {
			if completed[pending.ID] {
				continue
			}
			item, file, err := f.s.Get(context.Background(), f.p, pending.ID)
			if err != nil {
				t.Fatal(err)
			}
			if item.State == "pending" {
				continue
			}
			if item.State != "ready" || file == nil {
				t.Fatalf("maximum image did not produce a thumbnail: %+v", item)
			}
			stat, statErr := file.Stat()
			decoded, decodeErr := jpeg.Decode(file)
			file.Close()
			if statErr != nil || decodeErr != nil {
				t.Fatalf("invalid thumbnail output: %v, %v", statErr, decodeErr)
			}
			if decoded.Bounds().Dx() != 320 || decoded.Bounds().Dy() != 320 || stat.Size() > maxOutput {
				t.Fatalf("unexpected thumbnail dimensions or size: %v, %d", decoded.Bounds(), stat.Size())
			}
			outputBytes += stat.Size()
			completed[item.ID] = true
		}
		if len(completed) < 2 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if len(completed) != 2 {
		t.Fatal("maximum image thumbnails did not finish within 40 seconds")
	}
	elapsed := time.Since(started)
	finishSampling()
	runtime.ReadMemStats(&after)
	peakHeap = max(peakHeap, after.HeapAlloc)
	runtime.GC()
	runtime.ReadMemStats(&retained)
	t.Logf("images=2 pixels_each=16000000 decoded_bytes_each=64000000 png_bytes_each=%d jpeg_bytes_total=%d duration=%s heap_before=%d heap_after=%d heap_sampled_peak=%d heap_after_gc=%d total_alloc_delta=%d gc_cycles=%d", len(payload), outputBytes, elapsed, before.HeapAlloc, after.HeapAlloc, peakHeap, retained.HeapAlloc, after.TotalAlloc-before.TotalAlloc, after.NumGC-before.NumGC)
}
