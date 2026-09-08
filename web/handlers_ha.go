package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goeat/homeassistant"
	"goeat/middleware"
	"goeat/settings"
)

// haConfigForm is the payload from the setup dialog and Settings page.
type haConfigForm struct {
	BaseURL    string `json:"base_url"`
	Token      string `json:"token"`
	TodoEntity string `json:"todo_entity"`
	ItemFormat string `json:"item_format"`
}

func decodeHAForm(r *http.Request) haConfigForm {
	var f haConfigForm
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&f)
	} else {
		_ = r.ParseForm()
		f.BaseURL = r.FormValue("base_url")
		f.Token = r.FormValue("token")
		f.TodoEntity = r.FormValue("todo_entity")
		f.ItemFormat = r.FormValue("item_format")
	}
	f.BaseURL = strings.TrimRight(strings.TrimSpace(f.BaseURL), "/")
	f.Token = strings.TrimSpace(f.Token)
	f.TodoEntity = strings.TrimSpace(f.TodoEntity)
	f.ItemFormat = strings.TrimSpace(f.ItemFormat)
	return f
}

// handleHATest pings Home Assistant with the supplied (or stored) credentials.
//
//	POST /settings/ha/test  {base_url, token}
func (s *Server) handleHATest(w http.ResponseWriter, r *http.Request) {
	f := decodeHAForm(r)

	baseURL, token := f.BaseURL, f.Token
	if baseURL == "" || token == "" {
		hc := settings.LiveHAConfig(r.Context(), s.store, s.cfg, s.box)
		if baseURL == "" {
			baseURL = hc.BaseURL
		}
		if token == "" {
			token = hc.Token
		}
	}
	client := homeassistant.NewClient(baseURL, token)
	if client == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter a base URL and token."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleHAEntities lists the todo.* entities on the configured Home Assistant
// instance, for the setup dialog's "find lists" button.
//
//	POST /settings/ha/entities  {base_url, token}
func (s *Server) handleHAEntities(w http.ResponseWriter, r *http.Request) {
	f := decodeHAForm(r)

	baseURL, token := f.BaseURL, f.Token
	if baseURL == "" || token == "" {
		hc := settings.LiveHAConfig(r.Context(), s.store, s.cfg, s.box)
		if baseURL == "" {
			baseURL = hc.BaseURL
		}
		if token == "" {
			token = hc.Token
		}
	}
	client := homeassistant.NewClient(baseURL, token)
	if client == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter a base URL and token."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	entities, err := client.ListTodoEntities(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entities": entities})
}

// handleHASave persists the HA settings (token encrypted) then runs one sync.
//
//	POST /settings/ha/save  {base_url, token, todo_entity, item_format}
func (s *Server) handleHASave(w http.ResponseWriter, r *http.Request) {
	if s.box == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "encryption unavailable"})
		return
	}
	f := decodeHAForm(r)
	if f.BaseURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Base URL is required."})
		return
	}
	ctx := r.Context()
	_ = s.store.SetSetting(ctx, "HA_BASE_URL", f.BaseURL)
	if f.TodoEntity != "" {
		_ = s.store.SetSetting(ctx, "HA_TODO_ENTITY", f.TodoEntity)
	}
	if f.ItemFormat == "name" || f.ItemFormat == "name_qty" {
		_ = s.store.SetSetting(ctx, "HA_ITEM_FORMAT", f.ItemFormat)
	}
	if f.Token != "" {
		sealed, err := s.box.Seal(f.Token)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "encrypt failed"})
			return
		}
		if err := s.store.SetSecret(ctx, "HA_TOKEN", sealed); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "save failed"})
			return
		}
	}

	var actorID *int64
	if u := middleware.UserFromCtx(r); u != nil {
		id := u.ID
		actorID = &id
	}
	s.logEvent(r, actorID, "ha.saved", "setting", "HA_BASE_URL", f.BaseURL)

	res, err := homeassistant.SyncOnce(ctx, s.store, s.cfg, s.box)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": true, "synced": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "saved": true, "synced": true,
		"pushed": res.Pushed, "removed": res.Removed, "checked": res.Checked,
		"checked_ids": res.CheckedIDs, "unchecked_ids": res.UncheckedIDs,
	})
}

// handleListSync runs an on-demand push+pull for the current shopping list.
//
//	POST /list/sync
func (s *Server) handleListSync(w http.ResponseWriter, r *http.Request) {
	if middleware.HouseholdFromCtx(r) == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no household"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	res, err := homeassistant.SyncOnce(ctx, s.store, s.cfg, s.box)
	if err == homeassistant.ErrNotConfigured {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "needs_setup": true})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "pushed": res.Pushed, "removed": res.Removed,
		"checked": res.Checked, "unchecked": res.Unchecked,
		"checked_ids": res.CheckedIDs, "unchecked_ids": res.UncheckedIDs,
	})
}

// handleListSyncStatus reports whether HA sync is configured, for the UI to
// decide between opening the setup dialog and syncing directly.
//
//	GET /list/sync/status
func (s *Server) handleListSyncStatus(w http.ResponseWriter, r *http.Request) {
	hc := settings.LiveHAConfig(r.Context(), s.store, s.cfg, s.box)
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   hc.Configured(),
		"pull_enabled": hc.PullEnabled(),
		"interval":     hc.IntervalMinutes,
		"entity":       hc.TodoEntity,
	})
}
