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
