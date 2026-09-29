package downloadfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountInfoCandidatesDecodeAndExcludeUnsafeMounts(t *testing.T) {
	listing := strings.Join([]string{
		`36 25 8:1 /library\040one /mnt/Library\040One rw,relatime - ext4 /dev/sda1 rw`,
		`37 25 8:1 /readonly /mnt/readonly ro,relatime - ext4 /dev/sda1 rw`,
		`38 36 8:2 /inside /mnt/Library\040One/inside rw - ext4 /dev/sdb1 rw`,
		`39 25 0:4 / /mnt/system rw - proc proc rw`,
		`40 25 8:3 /data /mnt/super-ro rw - ext4 /dev/sdc1 ro`,
		`41 25 8:1 /file /mnt/file rw - ext4 /dev/sda1 rw`,
	}, "\n")
	records, err := parseMountInfo(strings.NewReader(listing))
	if err != nil {
		t.Fatal(err)
	}
	if records[0].root != "/library one" || records[0].target != "/mnt/Library One" {
		t.Fatal(records[0])
	}
	candidates := candidateRecords(records)
	if len(candidates) != 2 || candidates[0].target != "/mnt/Library One" || candidates[1].target != "/mnt/file" {
		t.Fatal(candidates)
	}
	// A file-shaped mountpoint is excluded during the filesystem check.
	for _, invalid := range []string{`1 2 0:1 / /mnt/bad\09 rw - ext4 x rw`, `1 2 0:1 / /mnt/bad\777 rw - ext4 x rw`, `1 2 0:1 / /mnt/bad rw - ext4`, `1 2 0:1 / /mnt/bad rw ext4 x rw`} {
		if _, err := parseMountInfo(strings.NewReader(invalid)); err == nil {
			t.Fatal("accepted malformed mountinfo", invalid)
		}
	}
}

func TestCandidateMustStillBeAnExistingDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if mount, err := OpenMount(file); err == nil {
		mount.Close()
		t.Fatal("regular file accepted as a mount root")
	}
}

func TestProtectedSourceOverlap(t *testing.T) {
	records := []mountRecord{
		{id: 1, device: "8:1", root: "/", target: "/"},
		{id: 2, device: "8:2", root: "/private/app", target: "/keys"},
		{id: 3, device: "8:3", root: "/private/library", target: "/downloads"},
	}
	for _, tc := range []struct {
		record    mountRecord
		protected bool
	}{
		{mountRecord{device: "8:2", root: "/private/app"}, true},
		{mountRecord{device: "8:2", root: "/private/app/child"}, true},
		{mountRecord{device: "8:2", root: "/private"}, true},
		{mountRecord{device: "8:2", root: "/private/application"}, false},
		{mountRecord{device: "8:4", root: "/private/app"}, false},
	} {
		if got := protectedSourceOverlap(tc.record, records, []string{"/keys"}); got != tc.protected {
			t.Fatalf("source %q: got %v", tc.record.root, got)
		}
	}
}
