package settings

import (
	"context"
	"strings"

	"goeat/config"
	"goeat/db"
	"goeat/scrape"
)

// RenderConfigFromConfig builds a renderer config from startup configuration.
// A bare FLARESOLVERR_URL (the pre-renderer setting) still works: it is read
// as a FlareSolverr backend so existing .env files keep functioning.
func RenderConfigFromConfig(cfg *config.Config) scrape.RenderConfig {
	rc := scrape.RenderConfig{
		Backend:         strings.TrimSpace(cfg.RenderBackend),
		URL:             strings.TrimSpace(cfg.RenderURL),
		Token:           strings.TrimSpace(cfg.RenderToken),
		FlareSolverrURL: strings.TrimSpace(cfg.FlareSolverrURL),
	}
	if rc.URL == "" && strings.TrimSpace(cfg.FlareSolverrURL) != "" {
		rc.Backend = scrape.RendererFlareSolverr
		rc.URL = strings.TrimSpace(cfg.FlareSolverrURL)
	}
	if rc.URL != "" && rc.Backend == "" {
		rc.Backend = scrape.RendererFlareSolverr // URL without a backend: assume the original one
	}
	return rc
}

// LiveRenderConfig reads the renderer settings straight from the settings
// table, falling back to the startup config for anything unset.
//
// Scraping resolves this per request rather than at boot so pointing the app
// at a new headless browser takes effect immediately - waiting for a restart
// to find out whether the URL was right makes the setting untestable.
func LiveRenderConfig(ctx context.Context, store db.Store, cfg *config.Config) scrape.RenderConfig {
	rc := RenderConfigFromConfig(cfg)
	if store == nil {
		return rc
	}
	admin := func(key string) (string, bool) {
		row, err := store.GetSetting(ctx, key)
		if err != nil || row == nil || row.Source != "admin" {
			return "", false
		}
		return strings.TrimSpace(row.Value), true
	}
	if v, ok := admin("RENDER_BACKEND"); ok {
		rc.Backend = v
	}
	if v, ok := admin("RENDER_URL"); ok {
		rc.URL = v
	}
	if v, ok := admin("RENDER_TOKEN"); ok {
		rc.Token = v
	}
	if v, ok := admin("FLARESOLVERR_URL"); ok {
		rc.FlareSolverrURL = v
		if rc.URL == "" && v != "" {
			rc.Backend = scrape.RendererFlareSolverr
			rc.URL = v
		}
	}
	if rc.URL != "" && rc.Backend == "" {
		rc.Backend = scrape.RendererFlareSolverr
	}
	return rc
}
