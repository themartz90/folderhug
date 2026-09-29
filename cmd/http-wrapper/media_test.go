package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerKeySetupNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker", "key")
	if err := setupMedia(path); err != nil {
		t.Fatal(err)
	}
	before, err := readWorkerKey(path)
	if err != nil || len(before) != 32 {
		t.Fatal(err)
	}
	if err = setupMedia(path); err == nil {
		t.Fatal("replaced key")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("changed key")
	}
	if err = os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readWorkerKey(path); err == nil {
		t.Fatal("accepted invalid key")
	}
}
