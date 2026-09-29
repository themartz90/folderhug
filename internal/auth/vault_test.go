package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultProfileBindingAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if _, err := LoadVault(path); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	v, err := CreateVault(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"username":"synthetic","password":"fixture secret"}`)
	first, err := v.Seal("one", plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := v.Seal("one", plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, plain) {
		t.Fatal("encryption did not hide or randomize secret")
	}
	if _, err := v.Open("two", first); !errors.Is(err, ErrCiphertext) {
		t.Fatal("cross-profile ciphertext accepted", err)
	}
	tampered := append([]byte(nil), first...)
	tampered[len(tampered)-1] ^= 1
	if _, err := v.Open("one", tampered); !errors.Is(err, ErrCiphertext) {
		t.Fatal("tampered ciphertext accepted", err)
	}
	if _, err := CreateVault(path); err == nil {
		t.Fatal("overwrote existing key")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("original key changed", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	wrong, err := LoadVault(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Open("one", first); !errors.Is(err, ErrCiphertext) {
		t.Fatal("wrong key accepted", err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadVault(path)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restored.Open("one", first)
	if err != nil || !bytes.Equal(plain, recovered) {
		t.Fatal("key recovery failed", err)
	}
	for _, data := range [][]byte{nil, {}, {1}, {2, 3, 4}} {
		if _, err := v.Open("one", data); !errors.Is(err, ErrCiphertext) {
			t.Fatal("malformed ciphertext accepted", err)
		}
	}
}
