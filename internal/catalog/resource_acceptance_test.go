package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/profiles"
)

func TestResourceCached10000ListingP95(t *testing.T) {
	if (os.Getenv("FOLDERHUG_RESOURCE_ACCEPTANCE") != "1" && os.Getenv("HTTPWRAPPER_RESOURCE_ACCEPTANCE") != "1") || runtime.GOOS != "linux" {
		t.Skip("set FOLDERHUG_RESOURCE_ACCEPTANCE=1 on Linux for synthetic latency measurements")
	}
	s, fetcher, profile, owner := fixture(t)
	var html strings.Builder
	html.WriteString("<h1>Index of /synthetic/</h1><hr><pre>\n")
	for i := range 10000 {
		fmt.Fprintf(&html, "<a href=\"file%05d.txt\">file%05d.txt</a> 02-Jan-2026 12:30 42\n", i, i)
	}
	html.WriteString("</pre><hr>")
	fetcher.body = html.String()
	query := Query{Limit: 500}
	first := browse(t, s, profile, owner, query)
	if first.TotalEntries != 10000 || len(first.Entries) != 500 || first.NextCursor == nil {
		t.Fatalf("incorrect cached fixture: total=%d page=%d", first.TotalEntries, len(first.Entries))
	}
	const warmups, samples = 5, 100
	durations := make([]time.Duration, 0, samples)
	for i := range warmups + samples {
		started := time.Now()
		page, err := s.Browse(t.Context(), profile.ID, profiles.LeaseID(owner), "root", query)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.NewEncoder(io.Discard).Encode(page); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(started)
		if page.TotalEntries != 10000 || len(page.Entries) != 500 || page.SnapshotID != first.SnapshotID || page.Stale {
			t.Fatal("measured request did not use the complete fresh cached snapshot")
		}
		if i >= warmups {
			durations = append(durations, elapsed)
		}
	}
	if fetcher.calls != 1 {
		t.Fatalf("cached measurement made %d source fetches", fetcher.calls)
	}
	slices.Sort(durations)
	t.Logf("process=test-harness cached_entries=10000 returned_entries=500 warmups=%d samples=%d p50=%s p95=%s p99=%s max=%s source_fetches=%d includes_json=true includes_http=false includes_browser=false target_p95=150ms", warmups, samples, durations[49], durations[94], durations[98], durations[99], fetcher.calls)
	if durations[94] >= 150*time.Millisecond {
		t.Fatalf("cached 10000-entry listing p95 %s reached the 150ms target", durations[94])
	}
}
