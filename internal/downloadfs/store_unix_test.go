//go:build unix

package downloadfs

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFIFOIsRejectedWithoutOpening(t *testing.T) {
	store, job, root := storageFixture(t)
	if err := unix.Mkfifo(filepath.Join(root, testJobID, ".part"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := job.OpenPart(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("FIFO accepted", err)
	}
	if _, err := store.UsedBytes(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("usage accepted FIFO", err)
	}
}
