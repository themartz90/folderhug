package scans

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/profiles"
)

func TestSubtreeOnlyCoverageIsPartial(t *testing.T) {
	f := setup(t, map[string][]string{"": {"nested/", "outside.txt"}, "nested/": {"child.txt"}})
	// A browse creates a safe directory identity without indexing the profile root.
	p := f.profile
	rootEntry, err := f.s.catalog.Entry(t.Context(), p.ID, "synthetic", "root")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := f.s.directory(t.Context(), job{Scan: Scan{ProfileID: p.ID}, Generation: p.Generation, LeaseID: "synthetic"}, rootEntry.EscapedPath)
	if err != nil {
		t.Fatal(err)
	}
	var dirID string
	for _, e := range entries {
		if e.Kind == "directory" {
			encoded, _ := json.Marshal(e)
			_, err = f.s.db.Exec("INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?)", p.ID, p.Generation, e.ID, e.ParentID, e.EscapedPath, string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			dirID = e.ID
		}
	}
	start(t, f)
	j := create(t, f, "subtree-only", Input{DirectoryID: dirID})
	wait(t, f.s, j.ID, "completed")
	page := search(t, f, "")
	if len(page.Entries) != 1 {
		t.Fatal("fixture index mismatch", page)
	}
	if page.Coverage[0].State != "partial" {
		t.Fatalf("profile root has indexed subtree, got coverage %q, want partial", page.Coverage[0].State)
	}
}

func TestAncestorCoverageUsesVisitedScope(t *testing.T) {
	for _, variant := range []string{"partial", "complete", "filtered", "cancelled", "interrupted", "unvisited", "excluded", "depth limit", "stale", "edited generation", "token scope"} {
		t.Run(variant, func(t *testing.T) {
			f := setup(t, map[string][]string{"": {"a/", "b/"}, "a/": {"known.txt"}, "b/": {"other.txt"}})
			ids := seedDirectoryIDs(t, f)
			input := Input{}
			want, wantEntries, scope := "partial", 1, "a/"
			switch variant {
			case "complete":
				want = "complete"
			case "filtered":
				input.Categories = []string{"document"}
			case "cancelled", "interrupted", "unvisited":
				f.source.block = "b/"
				if variant == "unvisited" {
					want, wantEntries, scope = "absent", 0, "b/"
				}
			case "excluded":
				input.ExcludedDirectories = []string{"a/"}
				want, wantEntries = "absent", 0
			case "depth limit":
				input.MaxDepth = integer(0)
				want, wantEntries = "absent", 0
			default:
				f.source.fail["b/"] = true
			}
			start(t, f)
			scan := create(t, f, "ancestor", input)
			if f.source.block != "" {
				deadline := time.After(5 * time.Second)
			waiting:
				for {
					select {
					case path := <-f.source.started:
						if path == "b/" {
							break waiting
						}
					case <-deadline:
						t.Fatal("scan did not reach the sibling after indexing a/")
					}
				}
				if variant != "interrupted" {
					if _, err := f.s.Action(t.Context(), f.owner, scan.ID, "cancel"); err != nil {
						t.Fatal(err)
					}
				}
				f.s.Close()
				wait(t, f.s, scan.ID, "cancelled", "interrupted")
			} else {
				wait(t, f.s, scan.ID, "partial", "completed")
			}
			if variant == "stale" {
				if _, err := f.s.db.Exec("UPDATE scan_jobs SET updated_at=? WHERE id=?", stamp(time.Now().Add(-25*time.Hour)), scan.ID); err != nil {
					t.Fatal(err)
				}
				want = "stale"
			}
			if variant == "edited generation" {
				profile, err := f.s.profiles.Patch(t.Context(), f.profile.ID, map[string]json.RawMessage{"root_url": json.RawMessage(`"https://new.example.test/synthetic/"`)}, f.owner)
				if err != nil {
					t.Fatal(err)
				}
				f.profile = profile
				ids = seedDirectoryIDs(t, f)
				want, wantEntries = "stale", 0
			}
			principal := f.owner
			if variant == "token scope" {
				_, raw, err := f.s.auth.CreateToken(t.Context(), auth.TokenCreate{Name: "reader", ProfileIDs: []string{f.profile.ID}, Scopes: []string{"browse:read"}})
				if err != nil {
					t.Fatal(err)
				}
				principal, err = f.s.auth.Authenticate(t.Context(), "", raw)
				if err != nil {
					t.Fatal(err)
				}
				other, err := f.s.profiles.Create(t.Context(), profiles.Input{Name: "Other", RootURL: "https://other.example.test/"}, f.owner)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.s.Search(t.Context(), principal, SearchQuery{ProfileID: other.ID}); !errors.Is(err, ErrNotFound) {
					t.Fatal("token saw another profile", err)
				}
			}
			page, err := f.s.Search(t.Context(), principal, SearchQuery{ProfileID: f.profile.ID, DirectoryID: ids[scope], Q: "known.txt"})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Entries) != wantEntries || len(page.Coverage) != 1 {
				t.Fatalf("unexpected scoped results: %+v", page)
			}
			coverage := page.Coverage[0]
			if coverage.State != want || coverage.ProfileID != f.profile.ID || coverage.DirectoryID != ids[scope] {
				t.Fatalf("coverage=%+v, want %s", coverage, want)
			}
			if want == "absent" {
				if coverage.LastScanAt != nil {
					t.Fatal("unvisited scope has a scan date")
				}
			} else {
				latest, err := f.s.load(t.Context(), scan.ID)
				if err != nil || coverage.LastScanAt == nil || !coverage.LastScanAt.Equal(latest.UpdatedAt) {
					t.Fatal("coverage is missing its scan date", coverage, err)
				}
			}
		})
	}
}

func TestCrashDowntimeDoesNotExhaustActiveBudget(t *testing.T) {
	f := setup(t, map[string][]string{"": {"file.txt"}})
	j := create(t, f, "crash", Input{MaxSeconds: integer(60)})
	_, err := f.s.db.Exec("UPDATE scan_jobs SET state='running',active_since=?,elapsed_seconds=0 WHERE id=?", stamp(time.Now().Add(-time.Hour)), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	start(t, f)
	recovered := wait(t, f.s, j.ID, "interrupted")
	t.Logf("recovered elapsed_seconds=%f", recovered.ElapsedSeconds)
	if _, err = f.s.Action(t.Context(), f.owner, j.ID, "resume"); err != nil {
		t.Fatalf("restart counted shutdown duration against active scan budget and cannot resume: %v", err)
	}
}

func seedDirectoryIDs(t *testing.T, f fixture) map[string]string {
	t.Helper()
	entries, err := f.s.directory(t.Context(), job{Scan: Scan{ProfileID: f.profile.ID}, Generation: f.profile.Generation, LeaseID: "synthetic"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, e := range entries {
		if e.Kind == "directory" {
			data, _ := json.Marshal(e)
			if _, err = f.s.db.Exec("INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?)", f.profile.ID, f.profile.Generation, e.ID, e.ParentID, e.EscapedPath, string(data)); err != nil {
				t.Fatal(err)
			}
			ids[e.EscapedPath] = e.ID
		}
	}
	return ids
}

func TestCoverageScopeEvidence(t *testing.T) {
	for _, variant := range []string{"complete", "filtered", "failed", "multiple descendants", "stale", "edited generation", "token scope"} {
		t.Run(variant, func(t *testing.T) {
			f := setup(t, map[string][]string{"": {"nested/", "sibling/"}, "nested/": {"child.txt"}, "sibling/": {"other.txt"}})
			ids := seedDirectoryIDs(t, f)
			input := Input{DirectoryID: ids["nested/"]}
			wantNested, wantRoot := "complete", "partial"
			if variant == "filtered" {
				input.Categories = []string{"document"}
				wantNested = "partial"
			}
			if variant == "failed" {
				f.source.fail["nested/"] = true
				wantNested = "partial"
			}
			start(t, f)
			j := create(t, f, "nested", input)
			wait(t, f.s, j.ID, "completed", "partial")
			if variant == "multiple descendants" {
				other := create(t, f, "sibling", Input{DirectoryID: ids["sibling/"]})
				wait(t, f.s, other.ID, "completed")
			}
			profile := f.profile
			if variant == "stale" {
				if _, err := f.s.db.Exec("UPDATE scan_jobs SET updated_at=? WHERE id=?", stamp(time.Now().Add(-25*time.Hour)), j.ID); err != nil {
					t.Fatal(err)
				}
				wantNested = "stale"
				wantRoot = "stale"
			}
			if variant == "edited generation" {
				profile.Generation++
				wantNested = "stale"
				wantRoot = "stale"
			}
			for _, scope := range []struct{ path, id, want string }{{"", "root", wantRoot}, {"nested/", ids["nested/"], wantNested}, {"sibling/", ids["sibling/"], "absent"}} {
				if variant == "multiple descendants" && scope.path == "sibling/" {
					scope.want = "complete"
				}
				coverage, err := f.s.coverage(t.Context(), profile, scope.id, scope.path)
				if err != nil || coverage.State != scope.want {
					t.Fatalf("%s: got %+v, want %s (%v)", scope.path, coverage, scope.want, err)
				}
				if scope.want != "absent" && coverage.LastScanAt == nil {
					t.Fatal("missing scan date")
				}
			}
			if variant == "token scope" {
				_, raw, err := f.s.auth.CreateToken(t.Context(), auth.TokenCreate{Name: "reader", ProfileIDs: []string{f.profile.ID}, Scopes: []string{"browse:read"}})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := f.s.auth.Authenticate(t.Context(), "", raw)
				if err != nil {
					t.Fatal(err)
				}
				page, err := f.s.Search(t.Context(), principal, SearchQuery{})
				if err != nil || len(page.Coverage) != 1 || page.Coverage[0].State != "partial" || len(page.Entries) != 1 {
					t.Fatalf("token coverage: %+v %v", page, err)
				}
			}
		})
	}
}
