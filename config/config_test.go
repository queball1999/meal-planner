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
