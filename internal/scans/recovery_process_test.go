package scans

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
)

type crashMarker struct {
	Directory, ProfileID, ScanID string
	Charged                      float64
}

func TestAbruptScanRestart(t *testing.T) {
	if os.Getenv("FOLDERHUG_SCAN_CRASH_CHILD") == "1" {
		f := setup(t, map[string][]string{"": {"root.txt", "nested/"}, "nested/": {"child.txt"}})
		f.source.block = "nested/"
		start(t, f)
		scan := create(t, f, "abrupt", Input{MaxSeconds: integer(60)})
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			j, err := f.s.load(t.Context(), scan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if j.DirectoriesVisited == 1 && j.ElapsedSeconds >= 30 {
				marker, _ := json.Marshal(crashMarker{f.directory, f.profile.ID, j.ID, j.ElapsedSeconds})
				if err = os.WriteFile(os.Getenv("FOLDERHUG_SCAN_CRASH_MARKER"), marker, 0600); err != nil {
					t.Fatal(err)
				}
				select {}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("child did not reach the reserved fetch")
	}
	if runtime.GOOS != "linux" || (os.Getenv("FOLDERHUG_SCAN_CRASH_ACCEPTANCE") != "1" && os.Getenv("HTTPWRAPPER_SCAN_CRASH_ACCEPTANCE") != "1") {
		t.Skip("set FOLDERHUG_SCAN_CRASH_ACCEPTANCE=1 on Linux for a disposable abrupt process stop")
	}
	temp := t.TempDir()
	markerPath := filepath.Join(temp, "marker.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestAbruptScanRestart$", "-test.timeout=20s")
	command.Env = append(os.Environ(), "TMPDIR="+temp, "FOLDERHUG_SCAN_CRASH_CHILD=1", "FOLDERHUG_SCAN_CRASH_MARKER="+markerPath)
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	var marker crashMarker
	found := false
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
		data, readErr := os.ReadFile(markerPath)
		if readErr == nil && json.Unmarshal(data, &marker) == nil {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatal("child did not publish its checkpoint")
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("child exited normally instead of being killed")
	}
	db, err := store.Open(marker.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.DB.Exec("UPDATE scan_jobs SET active_since=? WHERE id=?", stamp(time.Now().Add(-24*time.Hour)), marker.ScanID); err != nil {
		t.Fatal(err)
	}
	vault, err := auth.CreateVault(filepath.Join(temp, "recovery-key"))
	if err != nil {
		t.Fatal(err)
	}
	profileService := profiles.New(db.DB, vault)
	defer profileService.Close()
	authService := auth.New(db.DB)
	source := &fixtureSource{dirs: map[string][]string{"": {"root.txt", "nested/"}, "nested/": {"child.txt"}}, fail: map[string]bool{}, calls: map[string]int{}}
	service := New(db.DB, profileService, authService, catalog.New(db.DB, profileService, source), source, nil)
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	recovered := wait(t, service, marker.ScanID, "interrupted")
	if recovered.ElapsedSeconds != marker.Charged || recovered.ElapsedSeconds >= 60 {
		t.Fatalf("offline time charged: before=%f after=%f", marker.Charged, recovered.ElapsedSeconds)
	}
	owner := auth.Principal{Owner: true, CreatorID: "owner", SessionID: "synthetic"}
	if _, err = service.Action(t.Context(), owner, marker.ScanID, "resume"); err != nil {
		t.Fatal(err)
	}
	done := wait(t, service, marker.ScanID, "completed")
	page, err := service.Search(t.Context(), owner, SearchQuery{ProfileID: marker.ProfileID})
	if err != nil || len(page.Entries) != 3 || done.EntriesSeen != 3 {
		t.Fatalf("recovered index: %+v %+v %v", done, page, err)
	}
	t.Logf("abrupt_stop=SIGKILL offline_timestamp_shift=24h reserved_seconds=%.6f completed_seconds=%.6f index_rows=%d", marker.Charged, done.ElapsedSeconds, len(page.Entries))
}
