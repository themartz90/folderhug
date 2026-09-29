package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"os"
)

var ErrKey = errors.New("credential key is unavailable or invalid; restore the original key")
var ErrCiphertext = errors.New("credentials cannot be decrypted; restore the original key or re-enter credentials")

// Existing ciphertext uses this AAD. Changing it would make stored credentials unreadable.
const credentialAADPrefix = "http-wrapper:credentials:v1:"

type Vault struct{ aead cipher.AEAD }

func vaultFromKey(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKey
	}
	return &Vault{aead: aead}, nil
}

func LoadVault(path string) (*Vault, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 32 {
		return nil, ErrKey
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrKey
	}
	return vaultFromKey(key)
}

// CreateVault is for explicit setup only. Existing keys are never overwritten.
func CreateVault(path string) (*Vault, error) {
	key := randomBytes(32)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create credential key: %w", err)
	}
	if _, err = file.Write(key); err != nil {
		file.Close()
		return nil, ErrKey
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return nil, ErrKey
	}
	if err = file.Close(); err != nil {
		return nil, ErrKey
	}
	return vaultFromKey(key)
}

func (v *Vault) Seal(profileID string, plain []byte) ([]byte, error) {
	if v == nil || v.aead == nil {
		return nil, ErrKey
	}
	if profileID == "" {
		return nil, ErrInvalid
	}
	nonce := randomBytes(v.aead.NonceSize())
	envelope := append([]byte{1}, nonce...)
	return v.aead.Seal(envelope, nonce, plain, []byte(credentialAADPrefix+profileID)), nil
}

func (v *Vault) Open(profileID string, encrypted []byte) ([]byte, error) {
	if v == nil || v.aead == nil {
		return nil, ErrKey
	}
	if profileID == "" || len(encrypted) < 1+v.aead.NonceSize()+v.aead.Overhead() || encrypted[0] != 1 {
		return nil, ErrCiphertext
	}
	plain, err := v.aead.Open(nil, encrypted[1:1+v.aead.NonceSize()], encrypted[1+v.aead.NonceSize():], []byte(credentialAADPrefix+profileID))
	if err != nil {
		return nil, ErrCiphertext
	}
	return plain, nil
}
