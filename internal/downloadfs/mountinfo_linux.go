//go:build linux

package downloadfs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func currentMountInfo() ([]mountRecord, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseMountInfo(f)
}

func rootMountID(root *os.Root) (int, error) {
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return fileMountID(f)
}

func fileMountID(f *os.File) (int, error) {
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", f.Fd()))
	if err != nil {
		return 0, err
	}
	defer info.Close()
	scanner := bufio.NewScanner(info)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "mnt_id:"); ok {
			return strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, ErrUnsafe
}

func protectedAlias(record mountRecord, records []mountRecord, candidate os.FileInfo, protected []string) bool {
	verified := make([]string, 0, len(protected))
	for _, configured := range protected {
		if configured == "" {
			continue
		}
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return true
		}
		info, err := os.Stat(absolute)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || os.SameFile(candidate, info) {
			return true
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return true
		}
		verified = append(verified, filepath.ToSlash(resolved))
	}
	return protectedSourceOverlap(record, records, verified)
}

func checkedRecord(path string, root *os.Root, protected []string) (mountRecord, error) {
	records, err := currentMountInfo()
	if err != nil {
		return mountRecord{}, err
	}
	id, err := rootMountID(root)
	if err != nil {
		return mountRecord{}, err
	}
	info, err := root.Stat(".")
	if err != nil {
		return mountRecord{}, err
	}
	for _, record := range candidateRecords(records) {
		if record.target == path && record.id == id {
			if protectedAlias(record, records, info, protected) || unix.Access(path, unix.W_OK|unix.X_OK) != nil {
				return mountRecord{}, ErrUnsafe
			}
			return record, nil
		}
	}
	return mountRecord{}, ErrUnsafe
}

// DiscoverMounts finds mounted directories visible in this process's mount namespace.
func DiscoverMounts(protected []string) ([]DiscoveredMount, error) {
	records, err := currentMountInfo()
	if err != nil {
		return nil, err
	}
	discovered := make([]DiscoveredMount, 0)
	seen := map[string]bool{}
	for _, record := range candidateRecords(records) {
		if seen[record.target] {
			continue
		}
		info, err := os.Lstat(record.target)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		root, err := os.OpenRoot(record.target)
		if err != nil {
			continue
		}
		actual, statErr := root.Stat(".")
		id, idErr := rootMountID(root)
		root.Close()
		if statErr != nil || idErr != nil || !os.SameFile(info, actual) || id != record.id ||
			protectedAlias(record, records, info, protected) || unix.Access(record.target, unix.W_OK|unix.X_OK) != nil {
			continue
		}
		discovered = append(discovered, DiscoveredMount{Path: record.target, Label: filepath.Base(record.target)})
		seen[record.target] = true
	}
	return discovered, nil
}

func verifyOpenedMount(path string, root *os.Root, protected []string) (int, error) {
	record, err := checkedRecord(path, root, protected)
	if err != nil {
		return 0, err
	}
	return record.id, nil
}

func verifyMountChild(root *os.Root, expectedID int) error {
	id, err := rootMountID(root)
	if err != nil {
		return err
	}
	if id != expectedID {
		return ErrUnsafe
	}
	return nil
}

func verifyMountFile(file *os.File, expectedID int) error {
	id, err := fileMountID(file)
	if err != nil {
		return err
	}
	if id != expectedID {
		return ErrUnsafe
	}
	return nil
}
