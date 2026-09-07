// Package cryptbox provides authenticated symmetric encryption for secrets at
// rest (e.g. the Home Assistant long-lived token). The key is derived from the
// app's SESSION_SECRET, so rotating that secret invalidates stored ciphertext -
// which is acceptable: secrets can be re-entered, and a rotated session secret
// already logs everyone out.
package cryptbox

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
)

const prefix = "enc:v1:"

// Box seals and opens strings with a key derived from a passphrase.
type Box struct {
	key [32]byte
}

// New derives a Box key from passphrase (the app's SESSION_SECRET).
func New(passphrase string) *Box {
	return &Box{key: sha256.Sum256([]byte(passphrase))}
}

// IsSealed reports whether s looks like cryptbox ciphertext.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

// Seal encrypts plaintext and returns "enc:v1:<base64(nonce|ciphertext)>".
// An empty plaintext seals to "" so a blank secret stays blank.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", fmt.Errorf("cryptbox: nonce: %w", err)
	}
	sealed := secretbox.Seal(nonce[:], []byte(plaintext), &nonce, &b.key)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. A value without the prefix is returned unchanged, so
// callers can migrate plaintext rows lazily.
func (b *Box) Open(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if !strings.HasPrefix(s, prefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", fmt.Errorf("cryptbox: base64: %w", err)
	}
	if len(raw) < 24 {
		return "", errors.New("cryptbox: ciphertext too short")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	out, ok := secretbox.Open(nil, raw[24:], &nonce, &b.key)
	if !ok {
		return "", errors.New("cryptbox: decryption failed (wrong SESSION_SECRET?)")
	}
	return string(out), nil
}
