package config_test

import (
	"testing"

	"goeat/config"
)

func TestDefaults(t *testing.T) {
	t.Setenv("SESSION_SECRET", "test-secret-at-least-32-chars-long!")
	t.Setenv("APP_NAME", "")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("DATABASE_URL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppName != "Go Eat" {
		t.Errorf("AppName: want %q, got %q", "Go Eat", cfg.AppName)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr: want %q, got %q", ":8080", cfg.ListenAddr)
	}
	if cfg.DatabaseURL != "file:./data/goeat.db" {
		t.Errorf("DatabaseURL: want default, got %q", cfg.DatabaseURL)
	}
}

func TestSessionSecretRequired(t *testing.T) {
	t.Setenv("SESSION_SECRET", "explicit-secret-value-32-chars!!")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionSecret != "explicit-secret-value-32-chars!!" {
		t.Error("SessionSecret not loaded from environment")
	}
}

// TestSessionSecretPlaceholderRefused: the .env.example value is public, so
// starting with it must fail rather than run with a known secret (QSS §19).
func TestSessionSecretPlaceholderRefused(t *testing.T) {
	t.Setenv("SESSION_SECRET", "change-me-to-a-random-32-char-secret")
	if _, err := config.Load(); err == nil {
		t.Fatal("Load accepted the .env.example placeholder SESSION_SECRET")
	}
}

// TestSetupTokenValidation: SETUP_TOKEN must be 24+ hex characters, so a weak
// operator-chosen token fails at boot instead of guarding the wizard.
func TestSetupTokenValidation(t *testing.T) {
	cases := []struct {
		tok  string
		isOK bool
	}{
		{"", true}, // unset: a random one is generated at startup
		{"aabbccddeeff001122334455", true},
		{"AABBCCDDEEFF0011223344556677", true},
		{"aabbccddeeff00112233445", false},    // 23 chars
		{"changeme-changeme-changeme", false}, // not hex
	}
	for _, c := range cases {
		t.Setenv("SETUP_TOKEN", c.tok)
		cfg, err := config.Load()
		if (err == nil) != c.isOK {
			t.Errorf("SETUP_TOKEN=%q: err = %v, want ok=%v", c.tok, err, c.isOK)
		}
		if err == nil && cfg.SetupToken != c.tok {
			t.Errorf("SETUP_TOKEN=%q: cfg.SetupToken = %q", c.tok, cfg.SetupToken)
		}
	}
}

// TestDesktopRequiresLoopback: desktop mode must never listen on the network.
func TestDesktopRequiresLoopback(t *testing.T) {
	t.Setenv("GOEAT_DESKTOP", "1")
	for addr, isOK := range map[string]bool{
		"127.0.0.1:0":    true,
		"[::1]:8080":     true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
	} {
		t.Setenv("LISTEN_ADDR", addr)
		cfg, err := config.Load()
		if (err == nil) != isOK {
			t.Errorf("LISTEN_ADDR=%q: err = %v, want ok=%v", addr, err, isOK)
		}
		if err == nil && !cfg.Desktop {
			t.Errorf("LISTEN_ADDR=%q: Desktop not set", addr)
		}
	}
}
