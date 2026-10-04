package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
	"goeat/pricing"
)

// recipeOption is one entry in the "pick a replacement" list.
type recipeOption struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Servings int    `json:"servings"`
	Minutes  int    `json:"minutes"`
	Source   string `json:"source"`
}

// handleRecipeOptions lists the household's saved recipes for the slot-filling
// picker, optionally filtered by a search term.
//
// Served as JSON rather than baked into the plan page: a household with a few
// hundred saved recipes would otherwise ship all of them inside every day's
// dialog markup, seven times over.
func (s *Server) handleRecipeOptions(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	var (
		recipes []*db.CatalogRecipe
		err     error
	)
	if q != "" {
		recipes, err = s.store.SearchCatalogRecipes(ctx, hh.ID, q)
	} else {
		recipes, err = s.store.ListCatalogRecipes(ctx, hh.ID)
	}
	if err != nil {
		log.Printf("recipe options: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load recipes"})
		return
	}
	recipes = db.ExcludeLeftoverRecipes(recipes)

	// Capped: the picker is a short list to scan, not a browsable catalog -
	// /recipes is that. A household past the cap searches instead.
	const maxOptions = 50
	out := make([]recipeOption, 0, len(recipes))
	for i, rc := range recipes {
		if i >= maxOptions {
			break
		}
		out = append(out, recipeOption{
			ID:       rc.ID,
			Title:    rc.Title,
			Servings: rc.Servings,
			Minutes:  rc.PrepMinutes + rc.CookMinutes,
			Source:   rc.SourceKind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "recipes": out})
}

// pantryOption is one entry in the generate dialog's "already have it"
// picker. Label is also what gets posted back and handed to the LLM, so a
// stocked item carries its quantity ("Rice (2 kg)") and the plan knows how
// much there is to use up.
type pantryOption struct {
	Label    string  `json:"label"`
	Stocked  bool    `json:"stocked"` // on the Pantry page with a quantity, not just a known item
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity,omitempty"` // what the pantry says is on hand; 0 for a known-but-unstocked item
	Unit     string  `json:"unit,omitempty"`
}

// handlePantryOptions lists what the household is known to keep: pantry
// stock first (with quantities), then every other catalog item, for the
// generate dialog's Choices.js picker. JSON for the same reason as
// handleRecipeOptions - the dialog sits on several pages, and the catalog
// runs to hundreds of items once the seed list is in.
func (s *Server) handlePantryOptions(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	ctx := r.Context()

	stock, err := s.store.ListPantryItems(ctx, hh.ID)
	if err != nil {
		log.Printf("pantry options: stock: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load the pantry"})
		return
	}
	items, err := s.store.ListItems(ctx, hh.ID)
	if err != nil {
		log.Printf("pantry options: items: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load items"})
		return
	}

	out := make([]pantryOption, 0, len(stock)+len(items))
	// Catalog items already listed as stock are skipped below, matched by
	// link first and name second (an unlinked pantry row still names it).
	stockedIDs := map[int64]bool{}
	stockedNames := map[string]bool{}
	for _, pi := range stock {
		if pi.QuantityOnHand <= 0 {
			continue // an emptied row is a known item, not something on hand
		}
		qty := pricing.FormatQty(pi.QuantityOnHand)
		if pi.Unit != "" {
			qty += " " + pi.Unit
		}
		out = append(out, pantryOption{
			Label: pi.Name + " (" + qty + ")", Stocked: true,
			Name: pi.Name, Quantity: pi.QuantityOnHand, Unit: pi.Unit,
		})
		if pi.ItemID != nil {
			stockedIDs[*pi.ItemID] = true
		}
		stockedNames[strings.ToLower(pi.Name)] = true
	}
	for _, it := range items {
		if stockedIDs[it.ID] || stockedNames[strings.ToLower(it.Name)] {
			continue
		}
		out = append(out, pantryOption{Label: it.Name, Name: it.Name, Unit: it.StockUnit})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": out})
}

// handleMealFill puts a saved recipe into one slot of the current plan,
// scaled to whatever that day is feeding.
//
// The work itself is plan.MaterializeRecipe - shared with the agent's meal
// tools, so a meal created by hand and one created by the assistant are the
// same meal.
func (s *Server) handleMealFill(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	date := r.PathValue("date")
	slot := r.PathValue("slot")
	if date == "" || !validSlot(slot) {
		http.Error(w, "bad slot", http.StatusBadRequest)
		return
	}
	recipeID, err := strconv.ParseInt(r.FormValue("recipe_id"), 10, 64)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Pick a recipe first.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	// Scale to what the day is actually feeding. A day with no row falls back
	// to the household's own size, matching how the plan page displays it.
	portions := float64(hh.HouseholdSize)
	if day, _ := s.store.GetPlanDay(ctx, p.ID, date); day != nil && day.Portions > 0 {
		portions = day.Portions
	}

	res, err := plan.MaterializeRecipe(ctx, s.store, plan.MaterializeParams{
		PlanID:          p.ID,
		HouseholdID:     hh.ID,
		CatalogRecipeID: recipeID,
		Date:            date,
		Slot:            slot,
		Portions:        portions,
	})
	if err != nil {
		log.Printf("meal fill %s %s: %v", date, slot, err)
		s.setNotify(w, NotifyDanger, "Couldn't add that recipe to the plan.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	// A day being filled is a day being cooked; leaving it marked eating-out
	// would keep the new meal's ingredients off the shopping list, which is
	// not what someone who just picked a recipe meant.
	if err := s.store.SetPlanDayStatus(ctx, p.ID, date, db.DayCooking); err != nil {
		log.Printf("meal fill: reset day status %s: %v", date, err)
	}

	// Not quantity-only: this adds new meal content (a picked/generated
	// recipe), so its ingredients need a real price the same as generation.
	s.repriceInBackground(p.ID, hh, false)
	s.setNotify(w, NotifySuccess, mealFillMessage(res, slot, date))
	http.Redirect(w, r, "/plan", http.StatusSeeOther)
}

func validSlot(slot string) bool {
	return slot == "breakfast" || slot == "lunch" || slot == "dinner"
}

// mealFillMessage says what landed, and names any ingredient whose recipe
// amount was free text ("a pinch") - those cannot be priced, so the shopping
// list will be short by them unless the user fills the quantity in.
func mealFillMessage(res *plan.MaterializeResult, slot, date string) string {
	base := fmt.Sprintf("%s added to %s %s, scaled to %d servings. The shopping list is updating.",
		res.Title, dayLabel(date), slot, res.Servings)
	if len(res.Unquantified) == 0 {
		return base
	}
	return base + fmt.Sprintf(" %s had no amount in the recipe - set %s on the meal to get %s priced.",
		strings.Join(res.Unquantified, ", "),
		pluralize(len(res.Unquantified), "it", "them"),
		pluralize(len(res.Unquantified), "it", "them"))
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
