//go:build linux

package downloadfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGuardRejectsOrdinaryDirectoryAndNestedProcMount(t *testing.T) {
	ordinary := t.TempDir()
	if _, err := OpenMountChecked(ordinary, nil); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("ordinary directory became a mount: %v", err)
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, err := rootMountID(root)
	if err != nil {
		t.Fatal(err)
	}
	guarded := &Mount{root: root, guarded: true, mountID: id}
	if _, err := guarded.directory(filepath.ToSlash("proc")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("crossed /proc mount: %v", err)
	}
	procFile, err := os.Open("/proc/version")
	if err != nil {
		t.Fatal(err)
	}
	defer procFile.Close()
	if err := guarded.checkFile(procFile); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("accepted file on nested /proc mount: %v", err)
	}
}

func TestProtectedAliasThroughSymlink(t *testing.T) {
	base := t.TempDir()
	mountPath := filepath.Join(base, "mount")
	protectedPath := filepath.Join(mountPath, "app-data")
	if err := os.MkdirAll(protectedPath, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(protectedPath, alias); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}
	record := mountRecord{device: "1:2", root: "/", target: filepath.ToSlash(mountPath)}
	if !protectedAlias(record, []mountRecord{record}, info, []string{alias}) {
		t.Fatal("symlink to app data inside a mounted destination was accepted")
	}
}
