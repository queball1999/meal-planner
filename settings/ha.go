package settings

import (
	"context"
	"strconv"
	"strings"

	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
)

// HAConfig is the resolved Home Assistant sync configuration.
type HAConfig struct {
	BaseURL         string
	Token           string
	TodoEntity      string
	IntervalMinutes int
	ItemFormat      string // "name" | "name_qty"

	// HouseholdID is the one household whose shopping list syncs with this
	// HA to-do entity: whoever saved the HA settings last, from inside their
	// active household. 0 (installs from before multi-tenancy) means the
	// oldest household - see HouseholdFor.
	HouseholdID int64
}

// HAHouseholdKey is the settings row binding HA sync to one household.
const HAHouseholdKey = "HA_HOUSEHOLD_ID"

// HouseholdFor returns the id of the household HA syncs with.
func (c HAConfig) HouseholdFor(ctx context.Context, store db.Store) (int64, error) {
	if c.HouseholdID != 0 {
		return c.HouseholdID, nil
	}
	all, err := store.ListHouseholds(ctx)
	if err != nil || len(all) == 0 {
		return 0, err
	}
	return all[0].ID, nil
}

// Configured reports whether enough is set to talk to HA at all.
func (c HAConfig) Configured() bool { return c.BaseURL != "" && c.Token != "" }

// PullEnabled reports whether the background pull loop should run.
func (c HAConfig) PullEnabled() bool { return c.Configured() && c.IntervalMinutes > 0 }

const haTokenSecretKey = "HA_TOKEN"

// LiveHAConfig resolves the HA settings from the DB (admin overrides in the
// settings table, encrypted token in the secrets table) with the startup
// config as the fallback. Read per use so a Settings-page change - including
// enabling/disabling the pull - takes effect without a restart.
func LiveHAConfig(ctx context.Context, store db.Store, cfg *config.Config, box *cryptbox.Box) HAConfig {
	hc := HAConfig{
		BaseURL:         strings.TrimSpace(cfg.HABaseURL),
		Token:           strings.TrimSpace(cfg.HAToken),
		TodoEntity:      firstNonBlank(cfg.HATodoEntity, "todo.shopping_list"),
		IntervalMinutes: cfg.HASyncIntervalMinutes,
		ItemFormat:      firstNonBlank(cfg.HAItemFormat, "name_qty"),
	}
	if store == nil {
		return hc
	}
	admin := func(key string) (string, bool) {
		row, err := store.GetSetting(ctx, key)
		if err != nil || row == nil || row.Source != "admin" {
			return "", false
		}
		return strings.TrimSpace(row.Value), true
	}
	if v, ok := admin("HA_BASE_URL"); ok {
		hc.BaseURL = strings.TrimRight(v, "/")
	} else {
		hc.BaseURL = strings.TrimRight(hc.BaseURL, "/")
	}
	if v, ok := admin("HA_TODO_ENTITY"); ok && v != "" {
		hc.TodoEntity = v
	}
	if v, ok := admin("HA_ITEM_FORMAT"); ok && v != "" {
		hc.ItemFormat = v
	}
	if v, ok := admin("HA_SYNC_INTERVAL_MINUTES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			hc.IntervalMinutes = n
		}
	}
	if row, err := store.GetSetting(ctx, HAHouseholdKey); err == nil && row != nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(row.Value), 10, 64); err == nil {
			hc.HouseholdID = n
		}
	}
	if box != nil {
		if ct, ok, _ := store.GetSecret(ctx, haTokenSecretKey); ok && ct != "" {
			if tok, err := box.Open(ct); err == nil && tok != "" {
				hc.Token = tok
			}
		}
	}
	return hc
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
