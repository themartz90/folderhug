package scans

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPhaseReservationSurvivesRepeatedRestart(t *testing.T) {
	f := setup(t, map[string][]string{"": {"file.txt"}})
	scan := create(t, f, "repeated-crash", Input{MaxSeconds: integer(65)})
	for i, want := range []float64{30, 60, 65} {
		if _, err := f.s.db.Exec("UPDATE scan_jobs SET state='running' WHERE id=?", scan.ID); err != nil {
			t.Fatal(err)
		}
		j, err := f.s.load(t.Context(), scan.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, cancel, err := f.s.reservePhase(t.Context(), j, 0)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if _, err = f.s.db.Exec("UPDATE scan_jobs SET active_since=? WHERE id=?", stamp(time.Now().Add(-48*time.Hour)), scan.ID); err != nil {
			t.Fatal(err)
		}
		restarted := New(f.s.db, f.s.profiles, f.s.auth, f.s.catalog, f.s.fetcher, nil)
		if err = restarted.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		recovered := wait(t, restarted, scan.ID, "interrupted")
		restarted.Close()
		if recovered.ElapsedSeconds != want {
			t.Fatalf("restart %d charged %f, want %f", i, recovered.ElapsedSeconds, want)
		}
	}
	if _, err := f.s.Action(t.Context(), f.owner, scan.ID, "resume"); !errors.Is(err, ErrInvalidState) {
		t.Fatal("crashes reset the budget", err)
	}
}

func TestGracefulPhaseSettlementPreservesPausedState(t *testing.T) {
	f := setup(t, map[string][]string{"": {"file.txt"}})
	scan := create(t, f, "settlement", Input{})
	if _, err := f.s.db.Exec("UPDATE scan_jobs SET state='running',elapsed_seconds=4 WHERE id=?", scan.ID); err != nil {
		t.Fatal(err)
	}
	j, err := f.s.load(t.Context(), scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel, err := f.s.reservePhase(t.Context(), j, 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = f.s.Action(t.Context(), f.owner, scan.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	f.s.finish(j, "interrupted", "", 0.25)
	result, err := f.s.load(t.Context(), scan.ID)
	if err != nil || result.State != "paused" || result.ElapsedSeconds != 4.25 {
		t.Fatalf("settlement %+v %v", result, err)
	}
}

func TestCommittedDirectoryRetainsReservationWhenSettlementFails(t *testing.T) {
	f := setup(t, map[string][]string{"": {"file.txt"}})
	scan := create(t, f, "commit-crash", Input{})
	if _, err := f.s.db.Exec("UPDATE scan_jobs SET state='running' WHERE id=?", scan.ID); err != nil {
		t.Fatal(err)
	}
	j, err := f.s.load(t.Context(), scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, cancel, err := f.s.reservePhase(t.Context(), j, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	entries, err := f.s.directory(phase, j, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec("CREATE TRIGGER block_settlement BEFORE UPDATE ON scan_jobs WHEN NEW.active_since IS NULL AND OLD.active_since IS NOT NULL BEGIN SELECT RAISE(ABORT,'synthetic settlement failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err = f.s.persistDirectory(phase, j, "", 0, entries, time.Now()); err == nil {
		t.Fatal("settlement unexpectedly succeeded")
	}
	result, err := f.s.load(t.Context(), scan.ID)
	if err != nil || result.EntriesSeen != 1 || result.ElapsedSeconds != 30 {
		t.Fatalf("committed work lost reservation: %+v %v", result, err)
	}
	if _, err = f.s.db.Exec("DROP TRIGGER block_settlement"); err != nil {
		t.Fatal(err)
	}
	start(t, f)
	recovered := wait(t, f.s, scan.ID, "interrupted")
	if recovered.ElapsedSeconds != 30 {
		t.Fatal(recovered.ElapsedSeconds)
	}
	if _, err = f.s.Action(t.Context(), f.owner, scan.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	done := wait(t, f.s, scan.ID, "completed")
	if done.EntriesSeen != 1 || len(search(t, f, "").Entries) != 1 {
		t.Fatal("committed directory revisited or lost")
	}
}

func TestPhaseUsesRemainingBudget(t *testing.T) {
	f := setup(t, map[string][]string{"": {}})
	scan := create(t, f, "remaining", Input{MaxSeconds: integer(1)})
	if _, err := f.s.db.Exec("UPDATE scan_jobs SET state='running',elapsed_seconds=0.9 WHERE id=?", scan.ID); err != nil {
		t.Fatal(err)
	}
	j, err := f.s.load(t.Context(), scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, cancel, err := f.s.reservePhase(t.Context(), j, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	<-phase.Done()
	if !errors.Is(phase.Err(), context.DeadlineExceeded) {
		t.Fatal(phase.Err())
	}
	result, err := f.s.load(t.Context(), scan.ID)
	if err != nil || result.ElapsedSeconds != 1 {
		t.Fatal(result.ElapsedSeconds, err)
	}
}
