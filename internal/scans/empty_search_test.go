package scans

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
)

func TestEmptySearchRespectsDirectoryFiltersAndPagination(t *testing.T) {
	f := setup(t, map[string][]string{
		"":            {"scope/", "scope2/", "outside.txt"},
		"scope/":      {"Episode 10.mp4", "notes.txt", "deep/"},
		"scope/deep/": {"Episode 2.mp4"},
		"scope2/":     {"other.mp4"},
	})
	start(t, f)
	j := create(t, f, "empty-search", Input{})
	wait(t, f.s, j.ID, "completed")
	all, err := f.s.Search(t.Context(), f.owner, SearchQuery{})
	if err != nil || len(all.Entries) != 8 {
		t.Fatalf("empty query did not list the index: %+v, %v", all, err)
	}
	directoryID := ""
	for _, entry := range all.Entries {
		if entry.EscapedPath == "scope/" {
			directoryID = entry.ID
		}
	}
	if directoryID == "" {
		t.Fatal("fixture directory missing")
	}
	scoped := SearchQuery{ProfileID: f.profile.ID, DirectoryID: directoryID}
	page, err := f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || len(page.Entries) != 5 || len(page.Coverage) != 1 || page.Coverage[0].State != "complete" {
		t.Fatalf("scoped empty query lost children or coverage: %+v, %v", page, err)
	}
	for _, entry := range page.Entries {
		if !strings.HasPrefix(entry.EscapedPath, "scope/") {
			t.Fatalf("scoped query included sibling: %+v", entry)
		}
	}
	scoped.Match = "prefix"
	prefix, err := f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || !reflect.DeepEqual(prefix.Entries, page.Entries) {
		t.Fatalf("empty prefix differs from empty substring: %+v, %v", prefix, err)
	}
	scoped.Match = ""
	scoped.Query = catalog.Query{Category: "video", Limit: 1}
	first, err := f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || len(first.Entries) != 1 || first.Entries[0].Name != "Episode 2.mp4" || first.NextCursor == nil {
		t.Fatalf("category-only first page incorrect: %+v, %v", first, err)
	}
	scoped.Cursor = *first.NextCursor
	second, err := f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Name != "Episode 10.mp4" || second.NextCursor != nil {
		t.Fatalf("category-only next page incorrect: %+v, %v", second, err)
	}
	scoped.Q = "episode"
	if _, err := f.s.Search(t.Context(), f.owner, scoped); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("empty-query cursor accepted a different text query", err)
	}
	scoped.Cursor = ""
	scoped.Limit = 200
	scoped.Q, scoped.Match = "Episode 10", "prefix"
	nonempty, err := f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || len(nonempty.Entries) != 1 || nonempty.Entries[0].Name != "Episode 10.mp4" {
		t.Fatalf("nonempty prefix changed: %+v, %v", nonempty, err)
	}
	scoped.Q, scoped.Match = "10.mp", "substring"
	nonempty, err = f.s.Search(t.Context(), f.owner, scoped)
	if err != nil || len(nonempty.Entries) != 1 || nonempty.Entries[0].Name != "Episode 10.mp4" {
		t.Fatalf("nonempty substring changed: %+v, %v", nonempty, err)
	}
	for _, invalid := range []SearchQuery{
		{Q: strings.Repeat("a", 513)}, {Q: string([]byte{0xff})}, {DirectoryID: directoryID},
		{Match: "invalid"}, {Query: catalog.Query{Limit: 1001}}, {Query: catalog.Query{Limit: -1}},
	} {
		if _, err := f.s.Search(t.Context(), f.owner, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("search bounds were weakened", invalid, err)
		}
	}
}

func TestFindFilesScopeAndMultipleTypes(t *testing.T) {
	f := setup(t, map[string][]string{
		"":            {"scope/", "outside.txt"},
		"scope/":      {"direct.mp4", "notes.txt", "deep/"},
		"scope/deep/": {"nested.jpg"},
	})
	start(t, f)
	j := create(t, f, "find-files", Input{})
	wait(t, f.s, j.ID, "completed")
	q := SearchQuery{ProfileID: f.profile.ID, DirectoryID: "root", DirectOnly: true, Categories: []string{"video", "document"}}
	page, err := f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Name != "outside.txt" {
		t.Fatalf("root direct results: %+v, %v", page, err)
	}
	folders, err := f.s.Search(t.Context(), f.owner, SearchQuery{ProfileID: f.profile.ID, Query: catalog.Query{Category: "folder"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range folders.Entries {
		if entry.Name == "scope" {
			q.DirectoryID = entry.ID
		}
	}
	if q.DirectoryID == "root" {
		t.Fatal("missing indexed folder")
	}
	page, err = f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("folder direct results: %+v, %v", page, err)
	}
	q.DirectOnly = false
	q.Categories = []string{"video", "image"}
	page, err = f.s.Search(t.Context(), f.owner, q)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("recursive type results: %+v, %v", page, err)
	}
	q.Categories = []string{"video", "nonsense"}
	if _, err = f.s.Search(t.Context(), f.owner, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid category accepted", err)
	}
}

func TestEmptySearchRetainsTokenProfileAndCursorBoundaries(t *testing.T) {
	f := setup(t, map[string][]string{"": {"first.txt", "second.txt"}})
	start(t, f)
	j := create(t, f, "first-profile", Input{})
	wait(t, f.s, j.ID, "completed")
	other, err := f.s.profiles.Create(t.Context(), profiles.Input{Name: "Other", RootURL: "https://other.example.test/"}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	j, err = f.s.Create(t.Context(), f.owner, other.ID, "second-profile", Input{})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, f.s, j.ID, "completed")
	token, raw, err := f.s.auth.CreateToken(t.Context(), auth.TokenCreate{Name: "reader", ProfileIDs: []string{f.profile.ID}, Scopes: []string{"browse:read"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := f.s.auth.Authenticate(t.Context(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.s.Search(t.Context(), reader, SearchQuery{})
	if err != nil || len(page.Entries) != 2 || len(page.Coverage) != 1 || page.Coverage[0].ProfileID != f.profile.ID {
		t.Fatalf("unscoped token query widened visibility: %+v, %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.ProfileID != f.profile.ID {
			t.Fatal("foreign entry exposed", entry)
		}
	}
	if _, err := f.s.Search(t.Context(), reader, SearchQuery{ProfileID: other.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign profile accepted", err)
	}
	ownerPage, err := f.s.Search(t.Context(), f.owner, SearchQuery{Query: catalog.Query{Limit: 1}})
	if err != nil || ownerPage.NextCursor == nil {
		t.Fatal(ownerPage, err)
	}
	if _, err := f.s.Search(t.Context(), reader, SearchQuery{Query: catalog.Query{Limit: 1, Cursor: *ownerPage.NextCursor}}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal("owner cursor crossed token boundary", err)
	}
	if err := f.s.auth.DeleteToken(t.Context(), token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Search(t.Context(), reader, SearchQuery{}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked token listed index", err)
	}
}
