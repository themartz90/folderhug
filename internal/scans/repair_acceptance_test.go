package scans

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/testresource"
)

func TestRepairScan100000ConcurrentPruning(t *testing.T) {
	if runtime.GOOS != "linux" || (os.Getenv("FOLDERHUG_SCAN_REPAIR_ACCEPTANCE") != "1" && os.Getenv("HTTPWRAPPER_SCAN_REPAIR_ACCEPTANCE") != "1") {
		t.Skip("set FOLDERHUG_SCAN_REPAIR_ACCEPTANCE=1 on Linux for concurrent 100000-entry scans")
	}
	dirs := map[string][]string{"": {"part0/", "part1/"}}
	for part := range 2 {
		for i := range 49999 {
			path := fmt.Sprintf("part%d/", part)
			dirs[path] = append(dirs[path], fmt.Sprintf("file%05d.txt", part*49999+i))
		}
	}
	f := setup(t, dirs)
	other, err := f.s.profiles.Create(t.Context(), profiles.Input{Name: "Second origin", RootURL: "https://second.example.test/synthetic/"}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	second := f
	second.profile = other
	start(t, f)
	startTime := time.Now()
	firstJob := create(t, f, "large-one", Input{})
	secondJob := create(t, second, "large-two", Input{})
	await := func(id, state string) job {
		t.Helper()
		for deadline := time.Now().Add(120 * time.Second); time.Now().Before(deadline); {
			j, err := f.s.load(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if j.State == state {
				return j
			}
			if j.State != "queued" && j.State != "running" {
				t.Fatalf("large scan stopped: %+v", j)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("large scan timed out")
		return job{}
	}
	for _, id := range []string{firstJob.ID, secondJob.ID} {
		if j := await(id, "completed"); j.EntriesSeen != 100000 {
			t.Fatal(j.EntriesSeen)
		}
	}
	concurrentDuration := time.Since(startTime)
	f.source.mu.Lock()
	f.source.dirs["part0/"][0] = "new.txt"
	f.source.mu.Unlock()
	rescan := create(t, f, "prune", Input{})
	await(rescan.ID, "completed")
	var count int
	if err = f.s.db.QueryRow("SELECT count(*) FROM scan_index WHERE profile_id=?", f.profile.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	missing, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID, Q: "file00000.txt"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID, Q: "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 100000 || len(missing.Entries) != 0 || len(added.Entries) != 1 {
		t.Fatal("large prune corrupted the index", count)
	}
	untouched, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: other.ID, Q: "file00000.txt"})
	if err != nil || len(untouched.Entries) != 1 {
		t.Fatal("rescan changed the other profile", err)
	}
	page, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID})
	if err != nil || page.Coverage[0].State != "complete" {
		t.Fatal(page.Coverage, err)
	}
	f.source.mu.Lock()
	f.source.fail["part1/"] = true
	f.source.mu.Unlock()
	partial := create(t, f, "partial-coverage", Input{})
	await(partial.ID, "partial")
	folders, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID, Query: catalog.Query{Category: "folder"}})
	if err != nil || len(folders.Entries) != 2 {
		t.Fatal("large fixture directories missing", folders.Entries, err)
	}
	directoryID := ""
	for _, entry := range folders.Entries {
		if entry.EscapedPath == "part0/" {
			directoryID = entry.ID
		}
	}
	if directoryID == "" {
		t.Fatal("visited directory missing")
	}
	queryStarted := time.Now()
	for range 20 {
		page, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID, DirectoryID: directoryID, Q: "new.txt"})
		if err != nil || len(page.Entries) != 1 || len(page.Coverage) != 1 || page.Coverage[0].State != "partial" || page.Coverage[0].LastScanAt == nil {
			t.Fatal("large partial ancestor coverage mismatch", page.Coverage, err)
		}
	}
	queryDuration := time.Since(queryStarted)
	rss, err := testresource.RSS()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("concurrent_profiles=2 entries_per_profile=100000 largest_directory=49999 initial_seconds=%.3f total_seconds=%.3f process_rss_bytes=%d index_rows_primary=%d", concurrentDuration.Seconds(), time.Since(startTime).Seconds(), rss, count)
	t.Logf("scoped_searches=20 partial_ancestor=true total_query_seconds=%.3f mean_query_ms=%.3f", queryDuration.Seconds(), float64(queryDuration.Microseconds())/20000)
}
