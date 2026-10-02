package settings

import (
	"context"
	"strings"

	"goeat/config"
	"goeat/db"
)

// UpdateCheckKey is the on/off switch for the release check (phase 15).
const UpdateCheckKey = "UPDATE_CHECK"

// WhisperURLKey is the whisper.cpp server video imports transcribe with
// (phase 16).
const WhisperURLKey = "WHISPER_URL"

// LiveWhisperURL is the whisper server URL: the Settings page's value when an
// admin has set one (even to empty, which means "transcribe here"), else
// WHISPER_URL from .env. Read per use, so a change needs no restart.
func LiveWhisperURL(ctx context.Context, store db.Store, cfg *config.Config) string {
	if store != nil {
		if row, err := store.GetSetting(ctx, WhisperURLKey); err == nil && row != nil && row.Source == "admin" {
			return strings.TrimSpace(row.Value)
		}
	}
	return strings.TrimSpace(cfg.WhisperURL)
}

// LiveUpdateCheck reports whether the release check is on: the Settings
// page's value when an admin has set one, else UPDATE_CHECK from .env. Read
// before every check, so turning it off or on needs no restart.
func LiveUpdateCheck(ctx context.Context, store db.Store, cfg *config.Config) bool {
	if store != nil {
		if row, err := store.GetSetting(ctx, UpdateCheckKey); err == nil && row != nil && row.Source == "admin" {
			return config.ParseOnOff(row.Value, cfg.UpdateCheck)
		}
	}
	return cfg.UpdateCheck
}
