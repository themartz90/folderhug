package downloads

import (
	"testing"
)

func TestGuardedMountDoesNotOpenOrdinaryDirectory(t *testing.T) {
	root := t.TempDir()
	guarded := MountedRoot{ID: "auto", Label: "auto", Path: root, GuardMount: true}
	if mount, err := openMountedRoot(guarded); err == nil {
		mount.Close()
		t.Fatal("ordinary directory accepted as a guarded mount")
	}
	explicit := guarded
	explicit.GuardMount = false
	mount, err := openMountedRoot(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if err := mount.Close(); err != nil {
		t.Fatal(err)
	}
}
