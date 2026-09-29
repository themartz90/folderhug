package catalog

import (
	"errors"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/listing"
)

func number(v int64) *int64 { return &v }
func word(v string) *string { return &v }

func TestCombinedFiltersAndNullOrdering(t *testing.T) {
	date := time.Date(2026, 1, 2, 12, 30, 0, 0, time.UTC)
	entries := []listing.Entry{
		{ID: "a", Name: "Episode 10.MP4", Kind: "file", Category: "video", Extension: word("mp4"), Size: number(100), ModifiedAt: &date, TimezoneKnown: true},
		{ID: "b", Name: "Episode 2.mp4", Kind: "file", Category: "video", Extension: word("mp4"), Size: number(200), ModifiedAt: &date, TimezoneKnown: true},
		{ID: "c", Name: "Episode 1.mp4", Kind: "file", Category: "video", Extension: word("mp4"), Size: nil, ModifiedAt: nil},
		{ID: "d", Name: "Episode 3.txt", Kind: "file", Category: "document", Extension: word("txt"), Size: number(150), ModifiedAt: &date, TimezoneKnown: true},
		{ID: "e", Name: "Folder", Kind: "directory", Category: "folder"},
	}
	q, err := normalize(Query{Name: "EPISODE", Category: "video", Extensions: "MP4,mp4", MinSize: number(120), MaxSize: number(220), ModifiedAfter: &date, ModifiedBefore: &date})
	if err != nil {
		t.Fatal(err)
	}
	result := selected(entries, q)
	if len(result) != 1 || result[0].ID != "b" {
		t.Fatalf("filtered=%+v", result)
	}
	for _, direction := range []string{"asc", "desc"} {
		q, _ := normalize(Query{ShowFolders: boolPointer(false), Sort: "size", Direction: direction})
		result := selected(entries, q)
		if len(result) != 4 || result[3].ID != "c" {
			t.Fatalf("null position %s: %+v", direction, result)
		}
	}
	q, _ = normalize(Query{})
	result = selected(entries, q)
	if result[0].ID != "e" || result[1].ID != "c" || result[2].ID != "b" || result[4].ID != "a" {
		t.Fatalf("natural default=%+v", result)
	}
}

func TestInvalidQueriesAndNaturalNumbers(t *testing.T) {
	for _, q := range []Query{{Limit: -1}, {Limit: 1001}, {Sort: "url"}, {Direction: "sideways"}, {Category: "script"}, {Extensions: "mp4,,txt"}, {MinSize: number(-1)}, {MinSize: number(2), MaxSize: number(1)}, {Cursor: "x", Refresh: true}} {
		if _, err := normalize(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("query accepted: %+v", q)
		}
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{{"Episode 2", "Episode 10", -1}, {"file99999999999999999999999", "file100000000000000000000000", -1}, {"ITEM0002", "item2", 0}, {"Å2", "å10", -1}, {"a", "A", 0}} {
		got := natural(tc.a, tc.b)
		if got != tc.want {
			t.Fatalf("natural(%s,%s)=%d", tc.a, tc.b, got)
		}
	}
}

func TestCacheCapsAndEvictedCursor(t *testing.T) {
	s, _, p, owner := fixture(t)
	first := browse(t, s, p, owner, Query{Limit: 1})
	now := first.FetchedAt
	s.now = func() time.Time { return now }
	for i := 0; i < maxSnapshots; i++ {
		now = now.Add(time.Millisecond)
		browse(t, s, p, owner, Query{Refresh: true})
	}
	if len(s.snapshots) != maxSnapshots {
		t.Fatal("snapshot bound failed")
	}
	if _, err := s.Browse(t.Context(), p.ID, "session:"+owner.SessionID, "root", Query{Limit: 1, Cursor: *first.NextCursor}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("evicted cursor accepted", err)
	}
	if err := s.cache(&snapshot{entries: make([]listing.Entry, maxEntries+1)}); !errors.Is(err, ErrBudget) {
		t.Fatal("entry cap", err)
	}
	if err := s.cache(&snapshot{bytes: maxCacheBytes + 1}); !errors.Is(err, ErrBudget) {
		t.Fatal("byte cap", err)
	}
}
