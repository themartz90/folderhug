package scans

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/themartz90/folderhug/internal/listing"
)

func indexedDirectory(t *testing.T, f fixture, path string) string {
	t.Helper()
	if path == "" {
		return "root"
	}
	for _, entry := range search(t, f, "").Entries {
		if entry.Kind == "directory" && entry.EscapedPath == path {
			return entry.ID
		}
	}
	t.Fatalf("directory %q is not indexed", path)
	return ""
}

func indexSnapshot(t *testing.T, f fixture) string {
	t.Helper()
	encoded, err := json.Marshal(search(t, f, "").Entries)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func frontierSnapshot(t *testing.T, f fixture, id string) string {
	t.Helper()
	rows, err := f.s.db.Query("SELECT path_key,state FROM scan_frontier WHERE scan_id=? ORDER BY path_key", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	for rows.Next() {
		var path, state string
		if err := rows.Scan(&path, &state); err != nil {
			t.Fatal(err)
		}
		result.WriteString(path + ":" + state + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func TestOlderScanCannotContinueAfterOverlappingScan(t *testing.T) {
	for _, tc := range []struct {
		name, oldRoot, newerRoot, action     string
		oldCategories, newerCategories       []string
		newerPartial, newerExcluded, restart bool
	}{
		{name: "partial_resume_same_root", action: "resume"},
		{name: "cancelled_retry_same_root", action: "retry"},
		{name: "ancestor_after_newer_subtree", newerRoot: "a/", action: "resume"},
		{name: "subtree_after_newer_ancestor", oldRoot: "a/", action: "resume"},
		{name: "newer_partial_attempt", action: "resume", newerPartial: true},
		{name: "newer_excluded_attempt", action: "resume", newerExcluded: true},
		{name: "different_category_attempt", action: "resume", oldCategories: []string{"document"}, newerCategories: []string{"video"}},
		{name: "same_category_attempt", action: "retry", oldCategories: []string{"document"}, newerCategories: []string{"document"}},
		{name: "after_service_restart", action: "resume", restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, map[string][]string{
				"":   {"root-old.txt", "root-keep.txt", "a/", "ab/"},
				"a/": {"a-old.txt", "a-keep.txt", "deep/"}, "a/deep/": {"deep.txt"}, "ab/": {"b.txt"},
			})
			start(t, f)
			initial := create(t, f, "initial", Input{})
			wait(t, f.s, initial.ID, "completed")
			oldDirectory, newerDirectory := indexedDirectory(t, f, tc.oldRoot), indexedDirectory(t, f, tc.newerRoot)
			f.source.mu.Lock()
			f.source.fail["a/deep/"] = true
			f.source.mu.Unlock()
			old := create(t, f, "old", Input{DirectoryID: oldDirectory, Categories: tc.oldCategories})
			wait(t, f.s, old.ID, "partial")
			if tc.action == "retry" {
				if _, err := f.s.Action(t.Context(), f.owner, old.ID, "cancel"); err != nil {
					t.Fatal(err)
				}
			}
			f.source.mu.Lock()
			f.source.dirs[""] = []string{"root-new.txt", "root-keep.txt", "a/", "ab/"}
			f.source.dirs["a/"] = []string{"a-new.txt", "a-keep.txt", "deep/"}
			f.source.fail["a/deep/"] = tc.newerPartial
			f.source.mu.Unlock()
			newerInput := Input{DirectoryID: newerDirectory, Categories: tc.newerCategories}
			if tc.newerExcluded {
				newerInput.ExcludedDirectories = []string{"a/deep/"}
			}
			newer := create(t, f, "newer", newerInput)
			if tc.newerPartial || tc.newerExcluded {
				wait(t, f.s, newer.ID, "partial")
			} else {
				wait(t, f.s, newer.ID, "completed")
			}
			if !tc.newerPartial && !tc.newerExcluded && (len(tc.newerCategories) == 0 || tc.newerCategories[0] == "document") {
				if len(search(t, f, "a-new.txt").Entries) != 1 || len(search(t, f, "a-old.txt").Entries) != 0 {
					t.Fatal("newer scan did not record its additions and removals")
				}
			}
			f.source.mu.Lock()
			f.source.fail["a/deep/"] = false
			f.source.mu.Unlock()
			if tc.restart {
				f.s.Close()
				f.s = New(f.s.db, f.s.profiles, f.s.auth, f.s.catalog, f.source, nil)
				t.Cleanup(f.s.Close)
				start(t, f)
			}
			beforeIndex, beforeFrontier := indexSnapshot(t, f), frontierSnapshot(t, f, old.ID)
			if _, err := f.s.Action(t.Context(), f.owner, old.ID, tc.action); !errors.Is(err, ErrSuperseded) || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("older overlapping scan was allowed to continue: %v", err)
			}
			if after := indexSnapshot(t, f); after != beforeIndex {
				t.Fatal("rejected continuation erased or revived indexed observations")
			}
			if after := frontierSnapshot(t, f, old.ID); after != beforeFrontier {
				t.Fatal("rejected continuation changed its frontier")
			}
			current, err := f.s.Get(t.Context(), f.owner, old.ID)
			if err != nil || current.Error == nil || current.Error.Code != "scan_superseded" || !strings.Contains(current.Error.Message, "Start a new scan") {
				t.Fatalf("missing explicit restart guidance: %+v, %v", current.Error, err)
			}
			fresh := create(t, f, "fresh", Input{DirectoryID: oldDirectory})
			wait(t, f.s, fresh.ID, "completed")
			if len(search(t, f, "a-new.txt").Entries) != 1 || len(search(t, f, "a-old.txt").Entries) != 0 {
				t.Fatal("new scan did not preserve the newer source state")
			}
		})
	}
}

func TestDisjointScanDoesNotPreventContinuation(t *testing.T) {
	f := setup(t, map[string][]string{"": {"a/", "ab/"}, "a/": {"keep.txt", "deep/"}, "a/deep/": {"child.txt"}, "ab/": {"other.txt"}})
	start(t, f)
	initial := create(t, f, "initial", Input{})
	wait(t, f.s, initial.ID, "completed")
	a, ab := indexedDirectory(t, f, "a/"), indexedDirectory(t, f, "ab/")
	f.source.mu.Lock()
	f.source.fail["a/deep/"] = true
	f.source.mu.Unlock()
	old := create(t, f, "old", Input{DirectoryID: a})
	wait(t, f.s, old.ID, "partial")
	newer := create(t, f, "newer", Input{DirectoryID: ab})
	wait(t, f.s, newer.ID, "completed")
	f.source.mu.Lock()
	f.source.fail["a/deep/"] = false
	f.source.mu.Unlock()
	if _, err := f.s.Action(t.Context(), f.owner, old.ID, "resume"); err != nil {
		t.Fatal("disjoint scope prevented continuation", err)
	}
	wait(t, f.s, old.ID, "completed")
	for _, name := range []string{"keep.txt", "child.txt", "other.txt"} {
		if len(search(t, f, name).Entries) != 1 {
			t.Fatalf("continuation lost %s", name)
		}
	}
}

func TestSubtreeScanPreservesItsRootAndPrunesOnlyDescendants(t *testing.T) {
	for _, categories := range [][]string{nil, {"folder"}} {
		t.Run(strings.Join(append([]string{"categories"}, categories...), "_"), func(t *testing.T) {
			f := setup(t, map[string][]string{"": {"nested/", "nested-other/"}, "nested/": {"child.txt", "removed/"}, "nested/removed/": {"gone.txt"}, "nested-other/": {"sibling.txt"}})
			start(t, f)
			initial := create(t, f, "initial", Input{})
			wait(t, f.s, initial.ID, "completed")
			rootID := indexedDirectory(t, f, "nested/")
			f.source.mu.Lock()
			f.source.dirs["nested/"] = []string{"child.txt"}
			f.source.mu.Unlock()
			rescan := create(t, f, "subtree", Input{DirectoryID: rootID, Categories: categories})
			wait(t, f.s, rescan.ID, "completed")
			if indexedDirectory(t, f, "nested/") != rootID {
				t.Fatal("subtree root identity changed")
			}
			entries := search(t, f, "").Entries
			byPath := make(map[string]listing.Entry)
			for _, entry := range entries {
				byPath[entry.EscapedPath] = entry
			}
			if _, ok := byPath["nested/removed/"]; ok {
				t.Fatal("missing descendant directory was not pruned")
			}
			if _, ok := byPath["nested-other/sibling.txt"]; !ok {
				t.Fatal("subtree scan pruned a sibling")
			}
			_, gone := byPath["nested/removed/gone.txt"]
			if gone != (len(categories) > 0) {
				t.Fatal("pruning did not respect its category scope")
			}
		})
	}
}
