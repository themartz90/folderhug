package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/downloadfs"
)

func TestMountedPublicationRecoveryPreservesStopDecisions(t *testing.T) {
	for _, state := range []string{"running", "paused", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			h := fixture(t, 32<<10, Limits{})
			root := t.TempDir()
			h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
			j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "crash-window")
			if err != nil {
				t.Fatal(err)
			}
			dir, err := h.files.OpenJob(j.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			f, err := dir.OpenPart()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(h.data); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if err := dir.Publish(j.Name, int64(len(h.data))); err != nil {
				t.Fatal(err)
			}
			f, err = dir.OpenCompleted(j.Name)
			if err != nil {
				t.Fatal(err)
			}
			m, err := downloadfs.OpenMount(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Publish(context.Background(), "", j.ID, j.Name, f, int64(len(h.data)))
			f.Close()
			m.Close()
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(h.data)
			if _, err := h.db.DB.Exec(`UPDATE download_jobs SET state=?,publishing=1,transferred=?,total_bytes=?,checksum=? WHERE id=?`, state, len(h.data), len(h.data), hex.EncodeToString(hash[:]), j.ID); err != nil {
				t.Fatal(err)
			}
			h.start(t)
			got, err := h.s.Get(context.Background(), h.owner, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := state
			if state == "running" {
				want = "interrupted"
			}
			if got.State != want {
				t.Fatalf("startup changed %s to %s", state, got.State)
			}
			action := "resume"
			if state == "cancelled" {
				action = "retry"
			}
			if _, err := h.s.Action(context.Background(), h.owner, j.ID, action, false); err != nil {
				t.Fatal(err)
			}
			done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
			if done.State != "completed" || done.SavedName == nil || *done.SavedName != j.Name {
				t.Fatalf("recovery: %+v", done)
			}
			h.idle(t, j.ID)
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 1 {
				t.Fatalf("recovery left extra files: %v, %v", files, err)
			}
		})
	}
}

func TestMountedCopyAllowsQueueActions(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			h := fixture(t, 32<<10, Limits{})
			root := t.TempDir()
			h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			prepare := h.s.stageMount
			h.s.stageMount = func(ctx context.Context, j job) (*downloadfs.PreparedMount, error) {
				p, err := prepare(ctx, j)
				if err != nil {
					return nil, err
				}
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return p, nil
			}
			j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "stalled-copy")
			if err != nil {
				t.Fatal(err)
			}
			h.start(t)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("copy did not start")
			}
			created := make(chan error, 1)
			go func() {
				_, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "server"}, "other-job")
				created <- err
			}()
			select {
			case err := <-created:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("copy blocked another job creation")
			}
			acted := make(chan error, 1)
			go func() {
				if action == "close" {
					h.s.Close()
					acted <- nil
					return
				}
				_, err := h.s.Action(context.Background(), h.owner, j.ID, action, false)
				acted <- err
			}()
			select {
			case err := <-acted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatalf("copy blocked %s", action)
			}
			h.idle(t, j.ID)
			if _, err := os.Stat(filepath.Join(root, j.Name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stopped copy exposed final file: %v", err)
			}
		})
	}
}

func TestMountedDiskFullRetryReusesStaging(t *testing.T) {
	h := fixture(t, 32<<10, Limits{})
	h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: t.TempDir()}})
	prepare := h.s.stageMount
	failed := false
	h.s.stageMount = func(ctx context.Context, j job) (*downloadfs.PreparedMount, error) {
		if !failed {
			failed = true
			return nil, &os.PathError{Op: "write", Path: "synthetic", Err: syscall.ENOSPC}
		}
		return prepare(ctx, j)
	}
	j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "disk-full")
	if err != nil {
		t.Fatal(err)
	}
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" })
	h.idle(t, j.ID)
	h.fetcher.mu.Lock()
	before := h.fetcher.calls
	h.fetcher.mu.Unlock()
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "retry", false); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if done.State != "completed" {
		t.Fatalf("retry: %+v", done)
	}
	h.fetcher.mu.Lock()
	after := h.fetcher.calls
	h.fetcher.mu.Unlock()
	if before != after {
		t.Fatalf("retry downloaded again: %d -> %d", before, after)
	}
}

func TestMountedDeletePartialRemovesBothCopies(t *testing.T) {
	h := fixture(t, 32<<10, Limits{})
	root := t.TempDir()
	h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
	prepare := h.s.stageMount
	h.s.stageMount = func(ctx context.Context, j job) (*downloadfs.PreparedMount, error) {
		p, err := prepare(ctx, j)
		if err != nil {
			return nil, err
		}
		p.Close()
		return nil, ErrBudget
	}
	j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "remove-staging")
	if err != nil {
		t.Fatal(err)
	}
	h.start(t)
	h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" })
	h.idle(t, j.ID)
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 {
		t.Fatalf("missing mounted partial: %v %v", files, err)
	}
	if err := h.s.DeletePartial(context.Background(), h.owner, j.ID); err != nil {
		t.Fatal(err)
	}
	files, err = os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("mounted partial remains: %v %v", files, err)
	}
	dir, err := h.files.OpenJob(j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if f, err := dir.OpenCompleted(j.Name); err == nil {
		f.Close()
		t.Fatal("internal staging remains")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestMountedJobKeepsExistingNameAndServesPublishedFile(t *testing.T) {
	h := fixture(t, 64<<10, Limits{})
	root := t.TempDir()
	folder := filepath.Join(root, "Movies")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "file.bin"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
	page, err := h.s.Folders(true, "media", "")
	if err != nil || len(page.Folders) != 1 || page.Folders[0].Path != "Movies" {
		t.Fatalf("folders: %+v, %v", page, err)
	}
	if _, err := h.s.Folders(false, "media", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token folder access: %v", err)
	}
	j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media", DestinationPath: "Movies"}, "mounted-job")
	if err != nil {
		t.Fatal(err)
	}
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if done.State != "completed" || done.SavedName == nil || *done.SavedName != "file (1).bin" {
		t.Fatalf("download: %+v", done)
	}
	original, err := os.ReadFile(filepath.Join(folder, "file.bin"))
	if err != nil || string(original) != "existing" {
		t.Fatalf("original: %q, %v", original, err)
	}
	f, _, err := h.s.Completed(context.Background(), h.owner, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != string(h.data) {
		t.Fatalf("served file mismatch: %v", err)
	}
	h.idle(t, j.ID)
	dir, err := h.files.OpenJob(j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if internal, err := dir.OpenCompleted(j.Name); err == nil {
		internal.Close()
		t.Fatal("internal copy retained")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	part, err := dir.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(h.data); err != nil {
		t.Fatal(err)
	}
	part.Close()
	if err := dir.Publish(j.Name, int64(len(h.data))); err != nil {
		t.Fatal(err)
	}
	dir.Close()
	if err := h.s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir, err = h.files.OpenJob(j.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if internal, err := dir.OpenCompleted(j.Name); err == nil {
		internal.Close()
		t.Fatal("crash leftover retained")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	dir.Close()
	if err := os.Remove(filepath.Join(folder, *done.SavedName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, *done.SavedName), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DeleteCompleted(context.Background(), h.owner, j.ID); !errors.Is(err, ErrState) {
		t.Fatalf("mounted deletion: %v", err)
	}
	replacement, err := os.ReadFile(filepath.Join(folder, *done.SavedName))
	if err != nil || string(replacement) != "replacement" {
		t.Fatalf("replacement changed: %q, %v", replacement, err)
	}
	if _, err := os.Stat(filepath.Join(folder, "file.bin")); err != nil {
		t.Fatalf("original removed: %v", err)
	}
}

func TestMountedJobResumesAfterDestinationReturns(t *testing.T) {
	h := fixture(t, 32<<10, Limits{})
	root := filepath.Join(t.TempDir(), "mounted")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
	_, raw, err := h.auth.CreateToken(context.Background(), auth.TokenCreate{Name: "Synthetic", ProfileIDs: []string{h.profile.ID}, Scopes: []string{"downloads:write"}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.auth.Authenticate(context.Background(), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Create(context.Background(), token, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "token-mount"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token mount creation: %v", err)
	}
	j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "owner-mount")
	if err != nil {
		t.Fatal(err)
	}
	away := root + "-away"
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	interrupted := h.wait(t, j.ID, func(d Download) bool { return d.State == "interrupted" || d.State == "failed" })
	if interrupted.State != "interrupted" || interrupted.Error == nil || interrupted.Error.Code != "unavailable" {
		t.Fatalf("unavailable mount: %+v", interrupted)
	}
	if _, err := os.Stat(filepath.Join(away, "file.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected file in absent mount: %v", err)
	}
	h.fetcher.mu.Lock()
	calls := h.fetcher.calls
	h.fetcher.mu.Unlock()
	if err := os.Rename(away, root); err != nil {
		t.Fatal(err)
	}
	h.idle(t, j.ID)
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "resume", false); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if done.State != "completed" {
		t.Fatalf("resumed: %+v", done)
	}
	h.fetcher.mu.Lock()
	after := h.fetcher.calls
	h.fetcher.mu.Unlock()
	if after != calls {
		t.Fatalf("resume fetched upstream again: %d to %d", calls, after)
	}
}
func TestMountedCancelThenRetryAfterUnavailable(t *testing.T) {
	h := fixture(t, 32<<10, Limits{})
	root := filepath.Join(t.TempDir(), "mounted")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	h.s.ConfigureMounts([]MountedRoot{{ID: "media", Label: "Media", Path: root}})
	j, err := h.s.Create(context.Background(), h.owner, Input{ProfileID: h.profile.ID, EntryID: h.entry.ID, DestinationID: "media"}, "review-mounted-retry")
	if err != nil {
		t.Fatal(err)
	}
	away := root + "-away"
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	interrupted := h.wait(t, j.ID, func(d Download) bool { return d.State == "interrupted" || d.State == "failed" })
	if interrupted.State != "interrupted" {
		t.Fatalf("expected interrupted: %+v", interrupted)
	}
	h.idle(t, j.ID)
	if err := os.Rename(away, root); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "cancel", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Action(context.Background(), h.owner, j.ID, "retry", false); err != nil {
		t.Fatal(err)
	}
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if done.State != "completed" {
		t.Fatalf("retry did not complete: state=%s error=%+v", done.State, done.Error)
	}
}
