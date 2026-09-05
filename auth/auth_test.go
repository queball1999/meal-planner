package auth_test

import (
	"testing"

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
