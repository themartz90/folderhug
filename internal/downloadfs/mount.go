package downloadfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Mount struct{ root *os.Root }
type FolderEntry struct{ Name, Path string }

func OpenMount(path string) (*Mount, error) {
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	return &Mount{root: store.root}, nil
}

func (m *Mount) Close() error { return m.root.Close() }
func ValidFolder(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 2048 || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || !filepath.IsLocal(part) || !utf8.ValidString(part) || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func (m *Mount) directory(p string) (*os.Root, error) {
	if !ValidFolder(p) {
		return nil, ErrInvalid
	}
	if p == "" {
		return m.root.OpenRoot(".")
	}
	current, err := m.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(p, "/") {
		next, e := openDirectory(current, part)
		current.Close()
		if e != nil {
			return nil, e
		}
		current = next
	}
	return current, nil
}

func (m *Mount) Folders(p string) ([]FolderEntry, error) {
	root, err := m.directory(p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	folders := make([]FolderEntry, 0)
	seen := 0
	for {
		items, readErr := f.ReadDir(256)
		seen += len(items)
		if seen > 100000 {
			return nil, ErrLimit
		}
		for _, item := range items {
			name := item.Name()
			if !ValidFolder(name) || (!item.IsDir() && item.Type() != 0) {
				continue
			}
			info, e := root.Lstat(name)
			if e != nil {
				return nil, e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			folderPath := name
			if p != "" {
				folderPath = p + "/" + name
			}
			folders = append(folders, FolderEntry{name, folderPath})
			if len(folders) > 1000 {
				return nil, ErrLimit
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })
	return folders, nil
}

func (m *Mount) ValidateFolder(p string) error {
	root, err := m.directory(p)
	if err != nil {
		return err
	}
	return root.Close()
}

func (m *Mount) AvailableBytes() (*int64, error) {
	f, err := m.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := availableBytes(f)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// PreparedMount keeps the destination directory pinned between the slow copy
// and the final link. Close releases it without deleting recovery data.
type PreparedMount struct {
	root          *os.Root
	temp, desired string
	anchor        os.FileInfo
}

func (p *PreparedMount) Close() error { return p.root.Close() }

// Prepare syncs a job-specific temporary file without exposing a final name.
func (m *Mount) Prepare(ctx context.Context, folder, id, desired string, src *os.File, size int64) (_ *PreparedMount, err error) {
	if !validID(id) || !validName(desired) || size < 0 {
		return nil, ErrInvalid
	}
	root, err := m.directory(folder)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			root.Close()
		}
	}()
	// Incomplete mounted copies use this name across restart recovery and cleanup.
	temp := ".http-wrapper-" + id + ".part"
	if part, e := root.Lstat(temp); e == nil {
		if !part.Mode().IsRegular() {
			return nil, ErrUnsafe
		}
		probe, e := root.OpenFile(temp, os.O_RDONLY|safeOpenFlags, 0)
		if e != nil {
			return nil, e
		}
		actual, statErr := probe.Stat()
		links, linkErr := linkCount(probe)
		probe.Close()
		if statErr != nil {
			return nil, statErr
		}
		if linkErr != nil {
			return nil, linkErr
		}
		if !os.SameFile(part, actual) || links > 2 {
			return nil, ErrUnsafe
		}
		for n := 0; links == 2 && n < 10000; n++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if info, e := root.Lstat(collisionName(desired, n)); e == nil && os.SameFile(info, part) && info.Size() == size {
				return &PreparedMount{root, temp, desired, part}, nil
			}
		}
		if links == 2 {
			return nil, ErrUnsafe
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	file, err := openRegular(root, temp, os.O_RDWR, true)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		return nil, err
	}
	if _, err := io.Copy(file, &contextReader{ctx, io.LimitReader(src, size+1)}); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != size {
		return nil, ErrSize
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	return &PreparedMount{root, temp, desired, info}, nil
}

// Commit never replaces an existing destination file. The temporary link stays
// until the caller records completion, allowing recovery after a crash.
func (p *PreparedMount) Commit(ctx context.Context) (string, error) {
	part, err := p.root.Lstat(p.temp)
	if err != nil {
		return "", err
	}
	if !part.Mode().IsRegular() || !os.SameFile(part, p.anchor) || part.Size() != p.anchor.Size() {
		return "", ErrUnsafe
	}
	for n := 0; n < 10000; n++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := collisionName(p.desired, n)
		if info, e := p.root.Lstat(name); e == nil {
			if info.Mode().IsRegular() && os.SameFile(info, part) {
				return name, syncDirectory(p.root)
			}
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if err := p.root.Link(p.temp, name); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		return name, syncDirectory(p.root)
	}
	return "", ErrLimit
}

func (m *Mount) Publish(ctx context.Context, folder, id, desired string, src *os.File, size int64) (string, error) {
	p, err := m.Prepare(ctx, folder, id, desired, src, size)
	if err != nil {
		return "", err
	}
	defer p.Close()
	return p.Commit(ctx)
}

func (m *Mount) Cleanup(folder, id string) error {
	if !validID(id) {
		return ErrInvalid
	}
	root, err := m.directory(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	return removeRegular(root, ".http-wrapper-"+id+".part")
}

func (m *Mount) OpenCompleted(folder, name string) (*os.File, error) {
	if !validName(name) {
		return nil, ErrInvalid
	}
	root, err := m.directory(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openRegular(root, name, os.O_RDONLY, false)
}

func (m *Mount) OpenPublished(folder, name, id string) (*os.File, error) {
	if !validName(name) || !validID(id) {
		return nil, ErrInvalid
	}
	root, err := m.directory(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafe
	}
	f, err := root.OpenFile(name, os.O_RDONLY|safeOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		f.Close()
		return nil, ErrUnsafe
	}
	links, err := linkCount(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if links == 1 {
		return f, nil
	}
	if links == 2 {
		temp, e := root.Lstat(".http-wrapper-" + id + ".part")
		if e == nil && temp.Mode().IsRegular() && os.SameFile(info, temp) {
			return f, nil
		}
		links, e = linkCount(f)
		if e == nil && links == 1 {
			return f, nil
		}
	}
	f.Close()
	return nil, ErrUnsafe
}

func collisionName(name string, n int) string {
	if n == 0 {
		return name
	}
	dot := strings.LastIndex(name, ".")
	if dot < 1 {
		dot = len(name)
	}
	stem, ext, suffix := name[:dot], name[dot:], " ("+itoa(n)+")"
	for len(stem+suffix+ext) > 180 && stem != "" {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	return SafeName(stem + suffix + ext)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
