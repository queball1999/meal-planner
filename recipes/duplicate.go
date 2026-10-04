package recipes

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"goeat/db"
)

// DuplicateError is returned by an import when the household already has a
// recipe from the same source; ID is that recipe.
type DuplicateError struct {
	ID    int64
	Title string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("recipes: %q is already in your recipes", e.Title)
}

// FindBySource returns the household's recipe imported from any of urls,
// compared by SourceKey, or nil when there is none.
func FindBySource(ctx context.Context, store db.Store, householdID int64, urls ...string) (*db.CatalogRecipe, error) {
	want := map[string]bool{}
	for _, u := range urls {
		if k := SourceKey(u); k != "" {
			want[k] = true
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	all, err := store.ListCatalogRecipes(ctx, householdID)
	if err != nil {
		return nil, err
	}
	for _, cr := range all {
		if cr.SourceURL != "" && want[SourceKey(cr.SourceURL)] {
			return cr, nil
		}
	}
	return nil, nil
}

// checkDuplicate returns a *DuplicateError when the household already has a
// recipe from one of urls.
func checkDuplicate(ctx context.Context, store db.Store, householdID int64, urls ...string) error {
	cr, err := FindBySource(ctx, store, householdID, urls...)
	if err != nil {
		return fmt.Errorf("recipes: duplicate check: %w", err)
	}
	if cr != nil {
		return &DuplicateError{ID: cr.ID, Title: cr.Title}
	}
	return nil
}

// trackingParams are query parameters that say who shared a link, not which
// page it is.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true, "mc_cid": true, "mc_eid": true,
	"igsh": true, "igshid": true, "si": true, "feature": true, "ref": true, "ref_src": true,
	"is_from_webapp": true, "sender_device": true, "share_app_id": true, "share_link_id": true,
	"_r": true, "_t": true, "pp": true,
}

// SourceKey reduces a recipe URL to what identifies the recipe, so the same
// page shared two ways compares equal: scheme, "www."/"m.", a trailing slash,
// the fragment and tracking parameters are dropped. Video posts reduce to
// platform + post ID ("tiktok:7301…", "youtube:dQw4…"), so a Short and its
// watch?v= link match. "" for anything that isn't an http(s) URL.
func SourceKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	path := strings.TrimRight(u.EscapedPath(), "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	switch host {
	case "tiktok.com":
		// /@user/video/<id>
		for i, p := range parts {
			if p == "video" && i+1 < len(parts) {
				return "tiktok:" + parts[i+1]
			}
		}
	case "instagram.com":
		// /reel/<id>, /reels/<id>, /p/<id>, optionally under /<user>/
		for i, p := range parts {
			if (p == "reel" || p == "reels" || p == "p") && i+1 < len(parts) {
				return "instagram:" + parts[i+1]
			}
		}
	case "youtube.com":
		if v := u.Query().Get("v"); path == "/watch" && v != "" {
			return "youtube:" + v
		}
		if len(parts) == 2 && parts[0] == "shorts" {
			return "youtube:" + parts[1]
		}
	case "youtu.be":
		if len(parts) == 1 && parts[0] != "" {
			return "youtube:" + parts[0]
		}
	}

	q := u.Query()
	var keep []string
	for k, vs := range q {
		lk := strings.ToLower(k)
		if trackingParams[lk] || strings.HasPrefix(lk, "utm_") {
			continue
		}
		for _, v := range vs {
			keep = append(keep, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	sort.Strings(keep)
	key := host + path
	if len(keep) > 0 {
		key += "?" + strings.Join(keep, "&")
	}
	return key
}
