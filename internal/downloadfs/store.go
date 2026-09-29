// Package downloadfs confines download files to configured storage and job roots.
package downloadfs

import (
	"errors"
	"io"
	"math"
	"os"
	"sync"
)

var (
	ErrInvalid = errors.New("invalid download filename or job identifier")
	ErrUnsafe  = errors.New("download storage contains an unsafe filesystem object")
	ErrSize    = errors.New("partial download size does not match the completed file")
	ErrExists  = errors.New("completed download already exists")
	ErrLimit   = errors.New("download storage traversal limit exceeded")
)

type Store struct{ root *os.Root }
type Job struct {
	root *os.Root
	mu   sync.Mutex
}

// Open requires an existing directory. The opened root remains pinned across renames.
func Open(path string) (*Store, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafe
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafe
	}
	return &Store{root: root}, nil
}

func (s *Store) Close() error { return s.root.Close() }
func (j *Job) Close() error   { return j.root.Close() }

func (s *Store) OpenJob(id string, create bool) (*Job, error) {
	if !validID(id) {
		return nil, ErrInvalid
	}
	if create {
		if err := s.root.Mkdir(id, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	root, err := openDirectory(s.root, id)
	if err != nil {
		return nil, err
	}
	return &Job{root: root}, nil
}

func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafe
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafe
	}
	return root, nil
}

func openRegular(root *os.Root, name string, flag int, create bool) (*os.File, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		file, err := root.OpenFile(name, flag|os.O_CREATE|os.O_EXCL|safeOpenFlags, 0600)
		if err == nil {
			return checkedFile(root, name, file)
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafe
	}
	file, err := root.OpenFile(name, flag|safeOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	return checkedFile(root, name, file)
}

func checkedFile(root *os.Root, name string, file *os.File) (*os.File, error) {
	actual, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		file.Close()
		return nil, err
	}
	if !actual.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(actual, current) {
		file.Close()
		return nil, ErrUnsafe
	}
	links, err := linkCount(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if links != 1 {
		file.Close()
		return nil, ErrUnsafe
	}
	return file, nil
}

// OpenPart never truncates. Callers must close writable handles before Publish.
func (j *Job) OpenPart() (*os.File, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return openRegular(j.root, ".part", os.O_RDWR, true)
}

func (j *Job) ResetPart() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	file, err := openRegular(j.root, ".part", os.O_RDWR, false)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

// Publish links the synced partial file without replacement, then unlinks .part.
// It can finish an interrupted publication when exactly these two names share
// the expected regular file. OpenPart refuses the linked file during recovery.
func (j *Job) Publish(filename string, expectedSize int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !validName(filename) || expectedSize < 0 {
		return ErrInvalid
	}
	if target, err := j.root.Lstat(filename); err == nil {
		part, err := j.root.Lstat(".part")
		if err != nil || !target.Mode().IsRegular() || !part.Mode().IsRegular() || !os.SameFile(target, part) {
			return ErrExists
		}
		return j.finishPublication(filename, expectedSize, part)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := openRegular(j.root, ".part", os.O_RDWR, false)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if info.Size() != expectedSize {
		file.Close()
		return ErrSize
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := j.root.Link(".part", filename); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	return j.finishPublication(filename, expectedSize, info)
}

func (j *Job) finishPublication(filename string, expectedSize int64, anchor os.FileInfo) error {
	if !anchor.Mode().IsRegular() {
		return ErrUnsafe
	}
	file, err := j.root.OpenFile(".part", os.O_RDWR|safeOpenFlags, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	part, err := j.root.Lstat(".part")
	if err != nil {
		return err
	}
	target, err := j.root.Lstat(filename)
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || !part.Mode().IsRegular() || !target.Mode().IsRegular() || !os.SameFile(anchor, actual) || !os.SameFile(actual, part) || !os.SameFile(actual, target) {
		return ErrUnsafe
	}
	if actual.Size() != expectedSize || part.Size() != expectedSize || target.Size() != expectedSize {
		return ErrSize
	}
	links, err := linkCount(file)
	if err != nil {
		return err
	}
	if links != 2 {
		return ErrUnsafe
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := syncDirectory(j.root); err != nil {
		return err
	}
	// Close before unlinking for native Windows callers.
	if err := file.Close(); err != nil {
		return err
	}
	if err := j.root.Remove(".part"); err != nil {
		return err
	}
	return syncDirectory(j.root)
}

func (j *Job) OpenCompleted(filename string) (*os.File, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !validName(filename) {
		return nil, ErrInvalid
	}
	return openRegular(j.root, filename, os.O_RDONLY, false)
}

func (j *Job) RemovePart() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return removeRegular(j.root, ".part")
}
func (j *Job) RemoveCompleted(filename string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !validName(filename) {
		return ErrInvalid
	}
	return removeRegular(j.root, filename)
}

func removeRegular(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafe
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncDirectory(root)
}

func (s *Store) AvailableBytes() (*int64, error) {
	directory, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	n, err := availableBytes(directory)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// UsedBytes rejects symlinks and devices, and scans at most 100000 entries and 8 levels.
func (s *Store) UsedBytes() (int64, error) {
	entries := 0
	return usedBytes(s.root, 0, &entries)
}

func usedBytes(root *os.Root, depth int, entries *int) (int64, error) {
	if depth > 8 {
		return 0, ErrLimit
	}
	directory, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	var total int64
	for {
		items, readErr := directory.ReadDir(256)
		for _, item := range items {
			*entries++
			if *entries > 100000 {
				return 0, ErrLimit
			}
			info, err := root.Lstat(item.Name())
			if err != nil {
				return 0, err
			}
			var size int64
			switch {
			case info.IsDir():
				sub, err := openDirectory(root, item.Name())
				if err != nil {
					return 0, err
				}
				size, err = usedBytes(sub, depth+1, entries)
				sub.Close()
				if err != nil {
					return 0, err
				}
			case info.Mode().IsRegular():
				size = info.Size()
			default:
				return 0, ErrUnsafe
			}
			if size < 0 || total > math.MaxInt64-size {
				return 0, ErrLimit
			}
			total += size
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return 0, readErr
		}
	}
}
