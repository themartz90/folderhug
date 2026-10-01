package scans

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestDirectSearchIncludesChildFoldersAndExcludesSelectedFolder(t *testing.T) {
	f := setup(t, map[string][]string{
		"":              {"scope/", "outside.txt"},
		"scope/":        {"direct.txt", "nested/"},
		"scope/nested/": {"deep.txt"},
	})
	start(t, f)
	j := create(t, f, "direct-folders", Input{})
	wait(t, f.s, j.ID, "completed")
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"", []string{"outside.txt", "scope/"}},
		{"scope/", []string{"scope/direct.txt", "scope/nested/"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			page, err := f.s.Search(t.Context(), f.owner, SearchQuery{
				ProfileID: f.profile.ID, DirectoryID: indexedDirectory(t, f, tc.path), DirectOnly: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, entry := range page.Entries {
				paths = append(paths, entry.EscapedPath)
			}
			slices.Sort(paths)
			if !reflect.DeepEqual(paths, tc.want) {
				t.Fatalf("direct children = %v, want %v", paths, tc.want)
			}
		})
	}
}

func TestScanListReportsChangedProfileCoverageAsStale(t *testing.T) {
	f := setup(t, map[string][]string{"": {"file.txt"}})
	start(t, f)
	j := create(t, f, "generation-list", Input{})
	wait(t, f.s, j.ID, "completed")
	if _, err := f.s.profiles.Patch(t.Context(), f.profile.ID, map[string]json.RawMessage{
		"root_url": json.RawMessage(`"https://changed.example.test/"`),
	}, f.owner); err != nil {
		t.Fatal(err)
	}
	item, err := f.s.Get(t.Context(), f.owner, j.ID)
	if err != nil || item.Coverage.State != "stale" {
		t.Fatalf("scan detail = %+v, %v", item, err)
	}
	page, err := f.s.List(t.Context(), f.owner, f.profile.ID, 0, "")
	if err != nil || len(page.Scans) != 1 || page.Scans[0].Coverage.State != "stale" {
		t.Fatalf("scan list = %+v, %v", page, err)
	}
}
