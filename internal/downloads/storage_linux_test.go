//go:build linux

package downloads

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/downloadfs"
	"golang.org/x/sys/unix"
)

func storageFailureOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("FOLDERHUG_STORAGE_FAILURES") != "1" && os.Getenv("HTTPWRAPPER_STORAGE_FAILURES") != "1" {
		t.Skip("set FOLDERHUG_STORAGE_FAILURES=1 in an isolated non-root container")
	}
	if os.Geteuid() == 0 {
		t.Fatal("storage failure acceptance requires a non-root user")
	}
}

func TestStorageActualENOSPCBetweenSpaceChecks(t *testing.T) {
	storageFailureOptIn(t)
	base := t.TempDir()
	var volume unix.Statfs_t
	if err := unix.Statfs(base, &volume); err != nil {
		t.Fatal(err)
	}
	if volume.Type != unix.TMPFS_MAGIC || volume.Bsize <= 0 || volume.Blocks > uint64((32<<20)/volume.Bsize) || volume.Blocks == 0 {
		t.Fatal("refusing to fill storage: the test directory must be on a tmpfs volume no larger than 32 MiB")
	}
	root := filepath.Join(base, "downloads")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	files, err := downloadfs.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	const id = "synthetic-disk-full-job"
	dir, err := files.OpenJob(id, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	part, err := dir.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	defer part.Close()
	prefix := []byte("retained partial data")
	if _, err = part.Write(prefix); err != nil {
		t.Fatal(err)
	}
	if err = part.Sync(); err != nil {
		t.Fatal(err)
	}
	s := &Service{files: files, limits: defaults(Limits{StorageBudgetBytes: 1 << 20, FreeReserveBytes: -1}), reservations: make(map[string]*reservation)}
	if err = s.reserve(id, 128<<10, int64(len(prefix)), int64(len(prefix))); err != nil {
		t.Fatal(err)
	}
	defer s.release(id)
	filler, err := os.Create(filepath.Join(base, "external-consumer"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { filler.Close(); os.Remove(filler.Name()) }()
	if err = unix.Statfs(base, &volume); err != nil {
		t.Fatal(err)
	}
	available := int64(volume.Bavail) * volume.Bsize
	remaining := available - (32 << 10)
	if remaining <= 0 || remaining > 32<<20 {
		t.Fatal("the isolated tmpfs does not have sufficient bounded space for this test")
	}
	buffer := make([]byte, 64<<10)
	for remaining > 0 {
		n, writeErr := filler.Write(buffer[:min(int64(len(buffer)), remaining)])
		if writeErr != nil || n == 0 {
			t.Fatalf("could not prepare external space consumption: %v", writeErr)
		}
		remaining -= int64(n)
	}
	if err = filler.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = unix.Statfs(base, &volume); err != nil {
		t.Fatal(err)
	}
	available = int64(volume.Bavail) * volume.Bsize
	if available >= 64<<10 {
		t.Fatalf("test setup left too much free space: %d bytes", available)
	}
	// Another process can consume space immediately after a successful check.
	s.reservations[id].checked = time.Now()
	err = s.writePart(id, part, buffer, 4096)
	if !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("expected an actual filesystem ENOSPC, got %v", err)
	}
	state, problem, _ := classify(err)
	if state != "failed" || problem.Code != "storage_full" {
		t.Fatalf("disk-full classification: %s, %+v", state, problem)
	}
	got := make([]byte, len(prefix))
	if _, err = part.ReadAt(got, 0); err != nil || !bytes.Equal(got, prefix) {
		t.Fatalf("existing partial bytes were lost: %v", err)
	}
	if completed, err := dir.OpenCompleted("file.bin"); err == nil {
		completed.Close()
		t.Fatal("disk-full partial was published")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	t.Logf("actual ENOSPC after reservation: available_before_write=%d write_bytes=%d state=%s partial_retained=true", available, len(buffer), state)
}

func TestStoragePermissionFailureRetainsExistingFile(t *testing.T) {
	storageFailureOptIn(t)
	h := fixture(t, 100000, Limits{})
	if err := h.files.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files, err := downloadfs.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h.files, h.s.files = files, files
	j := h.create(t, 1, "permission", h.owner)
	dir, err := files.OpenJob(j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	jobPath := filepath.Join(root, j.ID)
	existing := filepath.Join(jobPath, "keep.txt")
	const original = "existing local data"
	if err = os.WriteFile(existing, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(jobPath, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(jobPath, 0700)
	h.start(t)
	done := h.wait(t, j.ID, func(d Download) bool { return d.State == "failed" || d.State == "completed" })
	if done.State != "failed" || done.Error == nil || done.Error.Code != "storage_full" || done.FileAvailable {
		t.Fatalf("permission failure was not contained: %+v, %+v", done, done.Error)
	}
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != original {
		t.Fatalf("permission failure damaged existing data: %v", err)
	}
	if _, err = os.Stat(filepath.Join(jobPath, j.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permission failure published a completed file: %v", err)
	}
	t.Log("actual read-only job directory: failed with storage_full; existing file retained")
}
