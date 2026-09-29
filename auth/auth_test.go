package auth_test

import (
	"strings"
	"testing"
	"time"

	"goeat/auth"
)

func TestHashAndCheck(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := auth.CheckPassword(hash, "correct-horse-battery-staple"); err != nil {
		t.Errorf("CheckPassword: expected nil, got %v", err)
	}

	if err := auth.CheckPassword(hash, "wrong-password"); err == nil {
		t.Error("CheckPassword: expected error for wrong password, got nil")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := auth.ValidatePassword("short"); err == nil {
		t.Error("expected error for short password")
	}
	if err := auth.ValidatePassword("exactly8"); err != nil {
		t.Errorf("expected nil for 8-char password, got %v", err)
	}
}

func TestGenerateToken(t *testing.T) {
	a, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	b, _ := auth.GenerateToken()
	if a == b {
		t.Error("two tokens should not be equal")
	}
	if len(a) != 64 { // 32 bytes → 64 hex chars
		t.Errorf("expected 64-char token, got %d", len(a))
	}
}

func TestHashToken(t *testing.T) {
	h1 := auth.HashToken("my-token")
	h2 := auth.HashToken("my-token")
	if h1 != h2 {
		t.Error("HashToken should be deterministic")
	}
	if auth.HashToken("a") == auth.HashToken("b") {
		t.Error("different tokens should produce different hashes")
	}
}

func TestValidatePasswordByteLimit(t *testing.T) {
	if err := auth.ValidatePassword(strings.Repeat("a", 72)); err != nil {
		t.Errorf("72 bytes should pass, got %v", err)
	}
	if err := auth.ValidatePassword(strings.Repeat("a", 73)); err == nil {
		t.Error("73 bytes should fail")
	}
	// 25 characters, 75 bytes: the limit is bytes, as bcrypt counts them.
	if err := auth.ValidatePassword(strings.Repeat("€", 25)); err == nil {
		t.Error("75 bytes of multi-byte characters should fail")
	}
}

// TestDummyCompareTakesBcryptTime: the unknown-username path must cost a real
// bcrypt compare. The old hard-coded dummy hash was malformed and returned
// instantly, so response time revealed which usernames exist.
func TestDummyCompareTakesBcryptTime(t *testing.T) {
	auth.CheckPasswordDummy("warm-up") // the one-time hash generation
	hash, _ := auth.HashPassword("x")

	start := time.Now()
	_ = auth.CheckPassword(hash, "wrong")
	real := time.Since(start)

	start = time.Now()
	auth.CheckPasswordDummy("wrong")
	dummy := time.Since(start)

	if dummy < real/4 {
		t.Errorf("dummy compare took %v, a real one %v - it should cost the same", dummy, real)
	}
}
