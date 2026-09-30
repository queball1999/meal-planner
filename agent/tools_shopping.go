package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"goeat/catalog"
	"goeat/db"
	"goeat/pricing"
)

// findListLine locates a shopping line by id or by display name.
//
// Same reasoning as findMeal: a user says "the olive oil", not "line 812". An
// ambiguous name errors with the candidates rather than picking one, because
// setting a price on the wrong line is silent and wrong for a whole week.
func findListLine(ctx context.Context, s *Session, planID, id int64, name string) (*db.ShoppingListItem, error) {
	lines, err := s.Store.ListShoppingListItems(ctx, planID)
	if err != nil {
		return nil, err
	}
	if id > 0 {
		for _, ln := range lines {
			if ln.ID == id {
				return ln, nil
			}
		}
		return nil, fmt.Errorf("no shopping line with id %d", id)
	}

	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return nil, fmt.Errorf("name an item on the list")
	}
	var exact, partial []*db.ShoppingListItem
	for _, ln := range lines {
		lt := strings.ToLower(ln.DisplayName)
		if lt == needle {
			exact = append(exact, ln)
		} else if strings.Contains(lt, needle) {
			partial = append(partial, ln)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = partial
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("nothing called %q on the shopping list", name)
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, ln := range hits {
			names = append(names, fmt.Sprintf("%q", ln.DisplayName))
		}
		return nil, fmt.Errorf("%q matches several lines (%s) - say which one", name, strings.Join(names, ", "))
	}
}

func dollars(cents int64) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

// RegisterShoppingTools adds the shopping-list verbs.
func RegisterShoppingTools(r *Registry) {
	r.Register(&Tool{
		Name:        "read_shopping_list",
		Description: "Read the current shopping list: every line with its quantity, price, whether it is already checked off, and whether the household already has it.",
		Run: func(ctx context.Context, s *Session, _ json.RawMessage) (Result, error) {
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			lines, err := s.Store.ListShoppingListItems(ctx, p.ID)
			if err != nil {
				return Result{}, err
			}

			var total int64
			out := make([]map[string]any, 0, len(lines))
			for _, ln := range lines {
				if !ln.InPantry {
					total += ln.LineTotalCents
				}
				out = append(out, map[string]any{
					"id": ln.ID, "name": ln.DisplayName,
					"quantity": ln.BuyQuantity, "unit": ln.PurchaseUnit,
					"price": dollars(ln.LineTotalCents),
					// Two different states, and conflating them in a summary
					// is how the assistant tells someone to buy what they
					// already own.
					"checked_off":  ln.Checked,
					"already_have": ln.InPantry,
				})
			}
			return Result{
				Summary: fmt.Sprintf("Read the shopping list: %d lines, %s to buy (budget %s).",
					len(out), dollars(total), dollars(p.BudgetCents)),
				Data: map[string]any{
					"lines": out, "total": dollars(total), "budget": dollars(p.BudgetCents),
					"over_budget": total > p.BudgetCents,
				},
			}, nil
		},
	})

	r.Register(&Tool{
		Name:        "add_list_item",
		Description: "Add a one-off item to the shopping list - something the household needs that no recipe called for.",
		Params: map[string]Param{
			"name":     {Type: "string", Description: "What to buy, as it would be written on a list."},
			"quantity": {Type: "number", Description: "How much. Defaults to 1."},
			"unit":     {Type: "string", Description: "The unit it is bought in (each, lb, bottle). Defaults to each."},
			"price":    {Type: "number", Description: "Price in dollars, if known. Leave out if not."},
		},
		Required: []string{"name"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Name     string  `json:"name"`
				Quantity float64 `json:"quantity"`
				Unit     string  `json:"unit"`
				Price    float64 `json:"price"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return Result{}, fmt.Errorf("what should be added?")
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			qty := a.Quantity
			if qty <= 0 {
				qty = 1
			}
			unit := strings.TrimSpace(a.Unit)
			if unit == "" {
				unit = "each"
			}
			cents := int64(math.Round(a.Price * 100))

			var itemID *int64
			if it, _ := catalog.EnsureItem(ctx, s.Store, s.HouseholdID, name); it != nil {
				itemID = &it.ID
			}

			if _, err := s.Store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
				PlanID:         p.ID,
				ItemID:         itemID,
				DisplayName:    name,
				BuyQuantity:    qty,
				PackSize:       1,
				NeedQuantity:   qty,
				PurchaseUnit:   unit,
				UnitPriceCents: cents,
				LineTotalCents: int64(math.Round(float64(cents) * qty)),
				PriceSource:    "manual",
				Confidence:     "manual",
			}); err != nil {
				return Result{}, err
			}
			summary := fmt.Sprintf("Added %s %s of %s to the shopping list.", pricing.FormatQty(qty), unit, name)
			if cents > 0 {
				summary += " at " + dollars(cents) + " each"
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "remove_list_item",
		Description: "Remove a line from the shopping list entirely. To say the household already owns something, use mark_already_have instead - that keeps it visible.",
		Params: map[string]Param{
			"line_id": {Type: "integer", Description: "The line's id, from read_shopping_list."},
			"name":    {Type: "string", Description: "The item's name, if you do not have its id."},
		},
		Mutates: true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				LineID int64  `json:"line_id"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			ln, err := findListLine(ctx, s, p.ID, a.LineID, a.Name)
			if err != nil {
				return Result{}, err
			}
			if err := s.Store.DeleteShoppingListItem(ctx, ln.ID); err != nil {
				return Result{}, err
			}
			return Result{Summary: fmt.Sprintf("Removed %q from the shopping list.", ln.DisplayName)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "mark_already_have",
		Description: "Mark a shopping line as something the household already has. It stays on the list for reference but is excluded from the total and not bought.",
		Params: map[string]Param{
			"line_id": {Type: "integer", Description: "The line's id."},
			"name":    {Type: "string", Description: "The item's name, if you do not have its id."},
			"have":    {Type: "boolean", Description: "true to mark it, false to un-mark. Defaults to true."},
		},
		Mutates: true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				LineID int64  `json:"line_id"`
				Name   string `json:"name"`
				Have   *bool  `json:"have"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			ln, err := findListLine(ctx, s, p.ID, a.LineID, a.Name)
			if err != nil {
				return Result{}, err
			}
			have := true
			if a.Have != nil {
				have = *a.Have
			}
			if err := s.Store.MarkShoppingListItemInPantry(ctx, ln.ID, have); err != nil {
				return Result{}, err
			}
			if have {
				return Result{Summary: fmt.Sprintf("Marked %q as already in the pantry - it is off the total.", ln.DisplayName)}, nil
			}
			return Result{Summary: fmt.Sprintf("%q is back on the list to buy.", ln.DisplayName)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "set_item_price",
		Description: "Set the known price of a shopping line, in dollars.",
		Params: map[string]Param{
			"line_id": {Type: "integer", Description: "The line's id."},
			"name":    {Type: "string", Description: "The item's name, if you do not have its id."},
			"price":   {Type: "number", Description: "Price in dollars for one unit of the line's purchase unit."},
		},
		Required: []string{"price"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				LineID int64   `json:"line_id"`
				Name   string  `json:"name"`
				Price  float64 `json:"price"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			if a.Price < 0 {
				return Result{}, fmt.Errorf("a price cannot be negative")
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			ln, err := findListLine(ctx, s, p.ID, a.LineID, a.Name)
			if err != nil {
				return Result{}, err
			}
			cents := int64(math.Round(a.Price * 100))
			packs := ln.BuyQuantity
			if ln.PackSize > 0 {
				packs = ln.BuyQuantity / ln.PackSize
			}
			if packs < 1 {
				packs = 1
			}
			if err := s.Store.UpdateShoppingListItemPrice(ctx, db.UpdateShoppingListItemPriceParams{
				ID:             ln.ID,
				StoreID:        ln.StoreID,
				ItemID:         ln.ItemID,
				BuyQuantity:    ln.BuyQuantity,
				PackSize:       ln.PackSize,
				PurchaseUnit:   ln.PurchaseUnit,
				UnitPriceCents: cents,
				LineTotalCents: int64(math.Round(float64(cents) * packs)),
				PriceSource:    "manual",
				Confidence:     "manual",
			}); err != nil {
				return Result{}, err
			}
			return Result{Summary: fmt.Sprintf("Set %q to %s per %s.", ln.DisplayName, dollars(cents), ln.PurchaseUnit)}, nil
		},
	})
}

// RegisterPantryTools adds the pantry and catalog verbs.
func RegisterPantryTools(r *Registry) {
	r.Register(&Tool{
		Name:        "read_pantry",
		Description: "Read what the household has on hand.",
		Run: func(ctx context.Context, s *Session, _ json.RawMessage) (Result, error) {
			rows, err := s.Store.ListPantryItems(ctx, s.HouseholdID)
			if err != nil {
				return Result{}, err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, r := range rows {
				out = append(out, map[string]any{
					"id": r.ID, "name": r.Name,
					"quantity": r.QuantityOnHand, "unit": r.Unit,
				})
			}
			return Result{
				Summary: fmt.Sprintf("Read the pantry: %d items on hand.", len(out)),
				Data:    map[string]any{"items": out},
			}, nil
		},
	})

	r.Register(&Tool{
		Name:        "set_pantry_qty",
		Description: "Set how much of something the household has on hand. Creates the pantry entry if it is not there yet.",
		Params: map[string]Param{
			"name":     {Type: "string", Description: "What it is, as a plain grocery name."},
			"quantity": {Type: "number", Description: "How much is on hand now. This replaces the current amount, it does not add to it."},
			"unit":     {Type: "string", Description: "The unit that amount is in. Defaults to whatever is already recorded, or each."},
		},
		Required: []string{"name", "quantity"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Name     string  `json:"name"`
				Quantity float64 `json:"quantity"`
				Unit     string  `json:"unit"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return Result{}, fmt.Errorf("what item?")
			}
			if a.Quantity < 0 {
				return Result{}, fmt.Errorf("a quantity cannot be negative")
			}
			term := pricing.Normalize(name)
			existing, _ := s.Store.GetPantryItemByTerm(ctx, s.HouseholdID, term)

			unit := strings.TrimSpace(a.Unit)
			if unit == "" && existing != nil {
				unit = existing.Unit
			}
			if unit == "" {
				unit = "each"
			}

			if existing != nil {
				// UpdatePantryItem sets the quantity outright. CreatePantryItem's
				// upsert would *add* to it, which is not what "set" means and
				// would inflate stock every time the assistant was asked.
				if err := s.Store.UpdatePantryItem(ctx, db.UpdatePantryItemParams{
					ID: existing.ID, QuantityOnHand: a.Quantity, Unit: unit,
				}); err != nil {
					return Result{}, err
				}
				return Result{Summary: fmt.Sprintf("%s is now %s %s on hand.", existing.Name, pricing.FormatQty(a.Quantity), unit)}, nil
			}

			pi, err := s.Store.CreatePantryItem(ctx, db.CreatePantryItemParams{
				HouseholdID: s.HouseholdID, Name: name, NormalizedTerm: term,
				QuantityOnHand: a.Quantity, Unit: unit,
			})
			if err != nil {
				return Result{}, err
			}
			if it, _ := catalog.EnsureItem(ctx, s.Store, s.HouseholdID, name); it != nil && pi != nil {
				_ = s.Store.SetPantryItemItem(ctx, pi.ID, &it.ID)
			}
			return Result{Summary: fmt.Sprintf("Added %s %s of %s to the pantry.", pricing.FormatQty(a.Quantity), unit, name)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "search_recipes",
		Description: "Search the household's saved recipes by title or tag. Use this to find a recipe id for fill_slot.",
		Params: map[string]Param{
			"query": {Type: "string", Description: "What to look for. Leave out to list everything."},
		},
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(args, &a)

			var (
				recipes []*db.CatalogRecipe
				err     error
			)
			if q := strings.TrimSpace(a.Query); q != "" {
				recipes, err = s.Store.SearchCatalogRecipes(ctx, s.HouseholdID, q)
			} else {
				recipes, err = s.Store.ListCatalogRecipes(ctx, s.HouseholdID)
			}
			if err != nil {
				return Result{}, err
			}
			recipes = db.ExcludeLeftoverRecipes(recipes)

			// Capped: a household with hundreds of recipes would otherwise
			// spend the whole context window on a list nobody reads.
			const max = 25
			out := make([]map[string]any, 0, max)
			for i, rc := range recipes {
				if i >= max {
					break
				}
				out = append(out, map[string]any{
					"id": rc.ID, "title": rc.Title, "servings": rc.Servings,
					"minutes": rc.PrepMinutes + rc.CookMinutes, "tags": rc.Tags,
				})
			}
			return Result{
				Summary: fmt.Sprintf("Found %d saved recipes.", len(out)),
				Data:    map[string]any{"recipes": out, "truncated": len(recipes) > max},
			}, nil
		},
	})

	r.Register(&Tool{
		Name:        "search_items",
		Description: "Search the household's grocery item catalog - the canonical names used for prices and pantry stock.",
		Params: map[string]Param{
			"query": {Type: "string", Description: "What to look for."},
		},
		Required: []string{"query"},
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			items, err := s.Store.ListItems(ctx, s.HouseholdID)
			if err != nil {
				return Result{}, err
			}
			matches := catalog.SuggestItems(a.Query, items, 10)
			out := make([]map[string]any, 0, len(matches))
			for _, m := range matches {
				out = append(out, map[string]any{"id": m.ItemID, "name": m.Name})
			}
			return Result{
				Summary: fmt.Sprintf("Found %d catalog items matching %q.", len(out), a.Query),
				Data:    map[string]any{"items": out},
			}, nil
		},
	})
}

// RegisterAll wires the whole tool surface. One entry point so a caller cannot
// accidentally build an agent that is missing half its verbs.
func RegisterAll(r *Registry) {
	RegisterPlanTools(r)
	RegisterShoppingTools(r)
	RegisterPantryTools(r)
}
