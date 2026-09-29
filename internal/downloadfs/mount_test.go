package downloadfs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestMountedExistingFolderNames(t *testing.T) {
	base := t.TempDir()
	names := []string{strings.Repeat("a", 181), "Cafe\u0301", " leading space"}
	if runtime.GOOS != "windows" {
		names = append(names, "Movies: 2024", "Series.", "CON")
	}
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m, err := OpenMount(base)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	items, err := m.Folders("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(names) {
		t.Fatalf("listed %d of %d existing folders: %+v", len(items), len(names), items)
	}
	for _, name := range names {
		if err := m.ValidateFolder(name); err != nil {
			t.Errorf("existing folder %q: %v", name, err)
		}
	}
}

func TestMountedFoldersAndKeepBoth(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "Movies"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Movies", "film.bin"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 1200; n++ {
		if err := os.WriteFile(filepath.Join(base, "file-"+strconv.Itoa(n)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "outside")); err != nil {
		t.Logf("symlink test skipped: %v", err)
	}
	m, err := OpenMount(base)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	items, err := m.Folders("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "Movies" {
		t.Fatalf("folders: %+v", items)
	}
	for _, bad := range []string{"../outside", "Movies/../outside", "/Movies", `Movies\outside`} {
		if _, err := m.Folders(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	source, err := os.CreateTemp(t.TempDir(), "source")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.WriteString("downloaded"); err != nil {
		t.Fatal(err)
	}
	const id = "syntheticmountjob0001"
	name, err := m.Publish(context.Background(), "Movies", id, "film.bin", source, 10)
	if err != nil || name != "film (1).bin" {
		t.Fatalf("publish %q: %v", name, err)
	}
	again, err := m.Publish(context.Background(), "Movies", id, "film.bin", source, 10)
	if err != nil || again != name {
		t.Fatalf("recovery %q: %v", again, err)
	}
	if err := m.Cleanup("Movies", id); err != nil {
		t.Fatal(err)
	}
	second, err := m.Publish(context.Background(), "Movies", "syntheticmountjob0002", "film.bin", source, 10)
	if err != nil || second != "film (2).bin" {
		t.Fatalf("second collision %q: %v", second, err)
	}
	if err := m.Cleanup("Movies", "syntheticmountjob0002"); err != nil {
		t.Fatal(err)
	}
	f, err := m.OpenCompleted("Movies", name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "downloaded" {
		t.Fatalf("content %q: %v", data, err)
	}
	original, err := os.ReadFile(filepath.Join(base, "Movies", "film.bin"))
	if err != nil || string(original) != "original" {
		t.Fatalf("original changed: %q, %v", original, err)
	}
}
