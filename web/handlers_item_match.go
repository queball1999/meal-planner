package web

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

// matchSuggestion is one candidate item offered for an unmatched line.
type matchSuggestion struct {
	ItemID  int64  `json:"item_id"`
	Name    string `json:"name"`
	Score   int    `json:"score"` // 0-100, for a confidence hint in the UI
	Pantry  bool   `json:"pantry"`
	Unit    string `json:"unit"`
	Reason  string `json:"reason"`
	Current bool   `json:"current"`
}

// handleItemMatchOptions suggests catalog items for a shopping-list line whose
// own item is only an auto-created placeholder.
//
// Fuzzy candidates come first, then the rest of the catalog so a line can
// always be matched to something even when the scorer finds nothing close -
// the whole point of the yellow chip is that a person can fix it.
func (s *Server) handleItemMatchOptions(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	ctx := r.Context()

	line := s.shoppingLineByID(ctx, hh.ID, id)
	if line == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "line not found"})
		return
	}

	items, err := s.store.ListItems(ctx, hh.ID)
	if err != nil {
		log.Printf("item match options: list items: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load the catalog"})
		return
	}
	pantryItems := s.pantryItemIDs(ctx, hh.ID)

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	needle := line.DisplayName
	if q != "" {
		needle = q
	}

	out := make([]matchSuggestion, 0, 12)
	seen := map[int64]bool{}
	for _, m := range catalog.SuggestItems(needle, items, 8) {
		seen[m.ItemID] = true
		out = append(out, matchSuggestion{
			ItemID: m.ItemID,
			Name:   m.Name,
			Score:  int(m.Score*100 + 0.5),
			Pantry: pantryItems[m.ItemID],
			Reason: "close match",
		})
	}
	// The rest of the catalog, so nothing is unmatchable. Only when the user
	// is actively searching, or when the scorer came up empty - otherwise a
	// good suggestion list would be buried under two hundred rows.
	if q != "" || len(out) == 0 {
		for _, it := range items {
			if seen[it.ID] || it.Source == "auto" {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(it.Name), strings.ToLower(q)) {
				continue
			}
			out = append(out, matchSuggestion{
				ItemID: it.ID,
				Name:   it.Name,
				Pantry: pantryItems[it.ID],
				Unit:   it.StockUnit,
			})
			if len(out) >= 30 {
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"line_name":   line.DisplayName,
		"suggestions": out,
	})
}

// handleItemMatch confirms that a shopping-list line means a particular
// catalog item.
//
// The line's placeholder item is merged into the chosen one rather than the
// line simply being repointed: without the merge, the placeholder survives
// holding its own price history and keeps collecting future ingredients with
// the same name, so the same line comes back yellow next week.
func (s *Server) handleItemMatch(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	itemID, err := strconv.ParseInt(r.FormValue("item_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "pick an item"})
		return
	}
	ctx := r.Context()

	line := s.shoppingLineByID(ctx, hh.ID, id)
	if line == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "line not found"})
		return
	}
	target, err := s.store.GetItem(ctx, itemID)
	if err != nil || target == nil || target.HouseholdID != hh.ID {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "item not found"})
		return
	}

	// Record what the line's name means, whether or not there is a placeholder
	// to merge - this is what stops the next plan re-creating it.
	term := pricing.Normalize(line.DisplayName)
	if term != "" {
		if err := s.store.CreateItemAlias(ctx, hh.ID, itemID, term, "manual"); err != nil {
			log.Printf("item match: alias %q: %v", term, err)
		}
	}

	if line.ItemID != nil && *line.ItemID != itemID {
		// Only a placeholder is merged away. Merging two real catalog items
		// because a shopping line was mis-linked would destroy an item the
		// household deliberately created.
		if old, _ := s.store.GetItem(ctx, *line.ItemID); old != nil && old.Source == "auto" {
			if err := s.store.MergeItems(ctx, hh.ID, old.ID, itemID); err != nil {
				log.Printf("item match: merge %d into %d: %v", old.ID, itemID, err)
			}
		}
	}

	if err := s.store.SetShoppingListItemItem(ctx, id, &itemID); err != nil {
		log.Printf("item match: relink line %d: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't save that match"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"item_id": itemID,
		"name":    target.Name,
		"message": line.DisplayName + " is now linked to " + target.Name + ".",
	})
}

// pantryItemIDs is the set of catalog items the household actually has on
// hand, used to mark a suggestion as something already in the pantry.
func (s *Server) pantryItemIDs(ctx context.Context, householdID int64) map[int64]bool {
	rows, err := s.store.ListPantryItems(ctx, householdID)
	if err != nil {
		log.Printf("pantry item ids: %v", err)
		return nil
	}
	out := make(map[int64]bool, len(rows))
	for _, p := range rows {
		if p.ItemID != nil {
			out[*p.ItemID] = true
		}
	}
	return out
}

// lineLinkState decides the colour of a shopping line's link chip.
//
//	"linked"    (green)  - the line resolves to a real catalog item, one the
//	                       household seeded, created, or confirmed.
//	"unmatched" (yellow) - the line is linked only to an auto-created
//	                       placeholder, or to nothing at all. Nothing is
//	                       broken, but the price and pantry stock for this
//	                       ingredient are being tracked under a name nobody
//	                       has confirmed, so it wants a person's eye.
func lineLinkState(item *db.Item) string {
	if item == nil || item.Source == "auto" {
		return "unmatched"
	}
	return "linked"
}
