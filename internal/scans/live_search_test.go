package scans

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
)

func TestSearchContinuesWhileSameProfileScanCommitsAndPrunesAnchor(t *testing.T) {
	f := setup(t, map[string][]string{"": {"b.txt", "d.txt", "nested/"}, "nested/": {}})
	start(t, f)
	wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
	q := SearchQuery{Q: ".txt", ProfileID: f.profile.ID, Query: catalog.Query{Limit: 1}}
	first, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || first.NextCursor == nil || first.Entries[0].Name != "b.txt" {
		t.Fatal(first, err)
	}
	f.source.mu.Lock()
	f.source.dirs[""] = []string{"a.txt", "c.txt", "d.txt", "nested/"}
	f.source.block = "nested/"
	f.source.release = make(chan struct{})
	f.source.mu.Unlock()
	rescan := create(t, f, "rescan", Input{})
	deadline := time.After(5 * time.Second)
	for {
		select {
		case path := <-f.source.started:
			if path == "nested/" {
				j, err := f.s.load(t.Context(), rescan.ID)
				if err == nil && j.DirectoriesVisited == 1 {
					goto committed
				}
			}
		case <-deadline:
			t.Fatal("rescan did not commit its first directory")
		}
	}
committed:
	q.Cursor = *first.NextCursor
	second, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Name != "c.txt" || second.NextCursor == nil {
		t.Fatal("active scan interrupted continuation", second, err)
	}
	close(f.source.release)
	wait(t, f.s, rescan.ID, "completed")
	// The original anchor has now been pruned. Its signed sort tuple still
	// resumes after b, including new c but excluding new a before the cursor.
	second, err = f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Name != "c.txt" || second.NextCursor == nil {
		t.Fatal("pruning anchor interrupted continuation", second, err)
	}
	q.Cursor = *second.NextCursor
	third, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(third.Entries) != 1 || third.Entries[0].Name != "d.txt" || third.NextCursor != nil {
		t.Fatal("live continuation lost ordering", third, err)
	}
}

func TestSearchCursorSurvivesOtherProfileScanAndOrdersTies(t *testing.T) {
	f := setup(t, map[string][]string{"": {"same.txt", "z.txt"}})
	start(t, f)
	wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
	q := SearchQuery{ProfileID: f.profile.ID, Query: catalog.Query{Limit: 1}}
	first, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	other, err := f.s.profiles.Create(t.Context(), profiles.Input{Name: "Other", RootURL: "https://other.example.test/"}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.s.Create(t.Context(), f.owner, other.ID, "other", Input{})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, f.s, j.ID, "completed")
	q.Cursor = *first.NextCursor
	page, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Name != "z.txt" {
		t.Fatal("unrelated scan interrupted continuation", page, err)
	}
	q = SearchQuery{Q: "same", Query: catalog.Query{Limit: 1}}
	page, err = f.s.Search(t.Context(), f.owner, q)
	if err != nil || page.NextCursor == nil {
		t.Fatal(page, err)
	}
	firstProfile := page.Entries[0].ProfileID
	q.Cursor = *page.NextCursor
	page, err = f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ProfileID <= firstProfile || page.NextCursor != nil {
		t.Fatal("equal names did not use profile tie breaker", page, err)
	}
}

func TestSearchCursorRejectsChangedGenerationAndTampering(t *testing.T) {
	f := setup(t, map[string][]string{"": {"a.txt", "b.txt"}})
	start(t, f)
	wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
	q := SearchQuery{ProfileID: f.profile.ID, Query: catalog.Query{Limit: 1}}
	first, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	q.Cursor = *first.NextCursor + "tampered"
	if _, err := f.s.Search(t.Context(), f.owner, q); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("tampered search cursor accepted", err)
	}
	parts := strings.Split(*first.NextCursor, ".")
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var valid pageCursor
	if err := json.Unmarshal(body, &valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*pageCursor){
		func(c *pageCursor) { c.Kind = "search"; c.Name = nil },
		func(c *pageCursor) { c.Expires = time.Now().Add(-time.Minute).Unix() },
		func(c *pageCursor) { c.Name = nil },
	} {
		c := valid
		mutate(&c)
		body, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, f.s.cursorKey)
		mac.Write(body)
		q.Cursor = base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if _, err := f.s.Search(t.Context(), f.owner, q); !errors.Is(err, ErrSnapshotExpired) {
			t.Fatal("legacy, expired or incomplete cursor accepted", err)
		}
	}
	if _, err := f.s.db.ExecContext(t.Context(), "UPDATE profiles SET generation=generation+1 WHERE id=?", f.profile.ID); err != nil {
		t.Fatal(err)
	}
	q.Cursor = *first.NextCursor
	if _, err := f.s.Search(t.Context(), f.owner, q); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("cursor crossed profile generation", err)
	}
}

func TestSearchCursorBoundsLongNames(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var entropy strings.Builder
	entropy.WriteByte('a')
	for i := 0; i < 3800; i++ {
		entropy.WriteByte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"[rng.IntN(62)])
	}
	for label, name := range map[string]string{
		"numeric expansion": "a" + strings.Repeat("1a", 1900) + ".txt",
		"high entropy":      entropy.String() + ".txt",
	} {
		t.Run(label, func(t *testing.T) {
			f := setup(t, map[string][]string{"": {name, "z.txt"}})
			start(t, f)
			wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
			q := SearchQuery{ProfileID: f.profile.ID, Query: catalog.Query{Limit: 1}}
			page, err := f.s.Search(t.Context(), f.owner, q)
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Name != name || page.NextCursor == nil {
				t.Fatal("long-name first page failed", err)
			}
			if len(*page.NextCursor) > MaxSearchCursorLength {
				t.Fatalf("unusable cursor emitted: %d", len(*page.NextCursor))
			}
			q.Cursor = *page.NextCursor
			page, err = f.s.Search(t.Context(), f.owner, q)
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Name != "z.txt" || page.NextCursor != nil {
				t.Fatal("long-name cursor failed to continue", err)
			}
			q.Cursor = strings.Repeat("x", MaxSearchCursorLength+1)
			if _, err := f.s.Search(t.Context(), f.owner, q); !errors.Is(err, ErrSnapshotExpired) {
				t.Fatal("oversized cursor accepted", err)
			}
		})
	}
}

func TestSearchCursorOrdersEqualNamesWithinProfile(t *testing.T) {
	f := setup(t, map[string][]string{"": {"left/", "right/"}, "left/": {"same.txt"}, "right/": {"same.txt"}})
	start(t, f)
	wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
	q := SearchQuery{Q: "same", ProfileID: f.profile.ID, Query: catalog.Query{Limit: 1}}
	first, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(first.Entries) != 1 || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	q.Cursor = *first.NextCursor
	second, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].ID <= first.Entries[0].ID || second.NextCursor != nil {
		t.Fatal("equal names did not use entry tie breaker", second, err)
	}
}

func TestSearchDoesNotEmitOversizedCursor(t *testing.T) {
	f := setup(t, map[string][]string{"": {"a.txt", "b.txt"}})
	start(t, f)
	wait(t, f.s, create(t, f, "initial", Input{}).ID, "completed")
	page := search(t, f, "a.txt")
	entry := page.Entries[0]
	// A malformed stored record must fail within the cursor budget rather than
	// return a token that the next page cannot accept.
	entry.Name = strings.Repeat("a", MaxSearchCursorLength)
	metadata, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.ExecContext(t.Context(), "UPDATE scan_index SET metadata=? WHERE profile_id=? AND entry_id=?", string(metadata), entry.ProfileID, entry.ID); err != nil {
		t.Fatal(err)
	}
	page, err = f.s.Search(t.Context(), f.owner, SearchQuery{Query: catalog.Query{Limit: 1}})
	if !errors.Is(err, ErrBudget) || page.NextCursor != nil {
		t.Fatal("oversized cursor was emitted", err)
	}
}
