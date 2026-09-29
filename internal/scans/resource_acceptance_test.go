package scans

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/testresource"
)

func TestResourceScan100000IdleRSS(t *testing.T) {
	if os.Getenv("HTTPWRAPPER_RESOURCE_ACCEPTANCE") != "1" || runtime.GOOS != "linux" {
		t.Skip("set HTTPWRAPPER_RESOURCE_ACCEPTANCE=1 on Linux for procfs resource measurements")
	}
	dirs := map[string][]string{"": {}}
	for folder := range 4 {
		path := fmt.Sprintf("part%d/", folder)
		dirs[""] = append(dirs[""], path)
		for i := range 24999 {
			dirs[path] = append(dirs[path], fmt.Sprintf("file%05d.txt", folder*24999+i))
		}
	}
	f := setup(t, dirs)
	start(t, f)
	started := time.Now()
	j := create(t, f, "resource-100000", Input{})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var done job
	for {
		var err error
		done, err = f.s.load(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if done.State == "completed" {
			break
		}
		if done.State != "queued" && done.State != "running" {
			t.Fatalf("scan did not complete: state=%s error=%v", done.State, done.Error)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	var count int
	if err := f.s.db.QueryRowContext(ctx, "SELECT count(*) FROM scan_index").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100000 || done.EntriesSeen != 100000 || done.FilesFound != 99996 {
		t.Fatalf("incorrect measured index: rows=%d seen=%d files=%d", count, done.EntriesSeen, done.FilesFound)
	}
	scanElapsed := time.Since(started)
	postScan, err := testresource.RSS()
	if err != nil {
		t.Fatal(err)
	}
	// The synthetic upstream's directory arrays are not part of a deployed app.
	// Keep the live services and database open, but release this source fixture.
	f.source.mu.Lock()
	f.source.dirs = nil
	f.source.mu.Unlock()
	dirs = nil
	runtime.GC()
	idle, err := testresource.MeasureRSS(time.Second, func() error {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-t.Context().Done():
			return t.Context().Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("process=test-harness index_rows=%d files=%d scan_duration=%s post_scan_rss_bytes=%d idle_initial_rss_bytes=%d idle_peak_rss_bytes=%d idle_final_rss_bytes=%d idle_samples=%d idle_seconds=10 forced_gc=true forced_os_release=false source_fixture_released=true target_idle_bytes=%d", count, done.FilesFound, scanElapsed, postScan, idle.Initial, idle.Peak, idle.Final, idle.Samples, 150<<20)
	if idle.Final >= 150<<20 {
		t.Fatalf("idle process RSS %d bytes reached the 150 MiB target", idle.Final)
	}
}
