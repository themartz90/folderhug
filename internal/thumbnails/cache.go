package thumbnails

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
)

func clearOldFiles(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	count := 0
	for {
		items, readErr := directory.ReadDir(128)
		for _, item := range items {
			count++
			if count > 20000 {
				return ErrBusy
			}
			if !managedName(item.Name()) {
				continue
			}
			info, err := root.Lstat(item.Name())
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return ErrInvalid
			}
			if err := root.Remove(item.Name()); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func openCacheFile(root *os.Root, name string) (*os.File, error) {
	if !managedName(name) {
		return nil, ErrNotFound
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalid
	}
	return file, nil
}

func writeCacheFile(root *os.Root, body []byte) (string, error) {
	if len(body) > maxOutput {
		return "", ErrImage
	}
	id := "thumbnail-" + rand.Text()
	temporary, complete := id+".tmp", id+".jpg"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(body); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err := root.Link(temporary, complete); err != nil {
		return "", err
	}
	return complete, nil
}

func (s *Service) removeFileLocked(name string, size int64) {
	if name == "" {
		return
	}
	if !managedName(name) {
		return
	}
	if s.refs[name] > 0 {
		s.retired[name] = size
		return
	}
	err := s.root.Remove(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.retired[name] = size
		return
	}
	s.bytes -= size
	if s.bytes < 0 {
		s.bytes = 0
	}
	delete(s.retired, name)
}

func (s *Service) evictLocked(required int64, except string) error {
	for name, size := range s.retired {
		if s.refs[name] == 0 {
			s.removeFileLocked(name, size)
		}
	}
	var count int
	if err := s.db.QueryRowContext(s.ctx, `SELECT count(*) FROM thumbnails`).Scan(&count); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(s.ctx, `SELECT id,file_name,size_bytes,expires_at FROM thumbnails WHERE state!='pending' AND id!=? ORDER BY accessed_at,id`, except)
	if err != nil {
		return err
	}
	type candidate struct {
		id, file      string
		size, expires int64
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.file, &item.size, &item.expires); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if s.bytes+required <= s.maxBytes && count <= maxRecords-50 && item.expires > s.now().Unix() {
			continue
		}
		if _, err := s.db.ExecContext(s.ctx, `DELETE FROM thumbnails WHERE id=?`, item.id); err != nil {
			return err
		}
		s.removeFileLocked(item.file, item.size)
		count--
	}
	if required > s.maxBytes || s.bytes > s.maxBytes-required || count > maxRecords-50 {
		return ErrBusy
	}
	return nil
}
