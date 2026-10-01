package settings

import (
	"context"

	"goeat/config"
	"goeat/db"
)

// UpdateCheckKey is the on/off switch for the release check (phase 15).
const UpdateCheckKey = "UPDATE_CHECK"

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
