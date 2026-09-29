// Package auth handles password hashing, token generation, and input
// validation. It has no internal dependencies - everything here is pure crypto.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12 // §9.1 - bcrypt cost 12

// HashPassword hashes password with bcrypt cost 12.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

// CheckPassword returns nil if password matches hash, bcrypt.ErrMismatchedHashAndPassword otherwise.
func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// MaxPasswordBytes is bcrypt's input limit. Longer passwords are refused
// here with a clear message rather than failing inside the hasher.
const MaxPasswordBytes = 72

// ValidatePassword enforces the minimum password policy (§9.1): at least 8
// characters and at most 72 bytes - bytes, not characters, since bcrypt
// counts UTF-8 bytes (QSS security design §7.1). Complexity is off by
// default on LAN.
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes (fewer characters if it uses accents, emoji or other non-ASCII letters)", MaxPasswordBytes)
	}
	return nil
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// CheckPasswordDummy spends the same bcrypt time as a real CheckPassword,
// against a hash made once at the real cost. Call it when the username
// doesn't exist, so response time can't tell the two cases apart (QSS
// security design §7.1). A hard-coded hash doesn't work: a malformed one
// fails instantly, and one at a different cost takes a different time.
func CheckPasswordDummy(password string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy password"), bcryptCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}

// GenerateToken returns 32 cryptographically random bytes as a hex string.
// This is the raw session token placed in the cookie.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the SHA-256 hex digest of token. This digest is what is
// stored in the sessions table - the raw token never touches the database.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
