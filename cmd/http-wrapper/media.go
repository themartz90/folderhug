package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

func setupMedia(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("cannot create worker key directory")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create worker key; an existing key is never replaced")
	}
	key := make([]byte, 32)
	rand.Read(key)
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("cannot save worker key")
	}
	fmt.Println("Media worker key created.")
	return nil
}

func readWorkerKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 32 {
		return nil, fmt.Errorf("media worker key is missing or invalid; run setup-media first")
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("cannot read media worker key")
	}
	return key, nil
}
