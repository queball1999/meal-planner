// Package catalog links free-text ingredient names to the household's items
// table (the canonical grocery-item catalog) and seeds the starter set. It sits
// above db and pricing so it can normalize terms without an import cycle.
package catalog

import (
	"context"
	"strings"

	"goeat/db"
	"goeat/pricing"
)

// EnsureItem returns the household's catalog item for name, creating a
// source='auto' row on first sight. A name that normalizes to empty yields
// (nil, nil) - the caller should just skip linking.
func EnsureItem(ctx context.Context, store db.Store, householdID int64, name string) (*db.Item, error) {
	term := pricing.Normalize(name)
	if term == "" {
		return nil, nil
	}
	if it, err := store.GetItemByTerm(ctx, householdID, term); err != nil {
		return nil, err
	} else if it != nil {
		return it, nil
	}
	return store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID:        householdID,
		Name:               strings.TrimSpace(name),
		NormalizedTerm:     term,
		StockUnit:          "each",
		DefaultPurchaseQty: 1,
		Source:             "auto",
	})
}

// LinkPlanIngredients ensures a catalog item for every unlinked ingredient row
// in a plan and writes back item_id + normalized_term. Idempotent: rows that
// already carry an item_id are left alone. Call it right after a plan is
// persisted, before pricing.
func LinkPlanIngredients(ctx context.Context, store db.Store, householdID, planID int64) error {
	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return err
	}
	for _, ing := range ings {
		if ing.ItemID != nil {
			continue
		}
		it, err := EnsureItem(ctx, store, householdID, ing.Name)
		if err != nil {
			return err
		}
		if it == nil {
			continue
		}
		id := it.ID
		if err := store.SetMealIngredientItem(ctx, ing.ID, &id, it.NormalizedTerm); err != nil {
			return err
		}
	}
	return nil
}

// BackfillHousehold links every unlinked meal-ingredient and pantry row for a
// household to a catalog item. Run once at boot so existing data picks up the
// item_id columns added in 00010_items.sql. shopping_list_items are rebuilt on
// the next re-cost, so they are not touched here.
func BackfillHousehold(ctx context.Context, store db.Store, householdID int64) error {
	plans, err := store.ListPlans(ctx, householdID)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if err := LinkPlanIngredients(ctx, store, householdID, p.ID); err != nil {
			return err
		}
	}

	pantry, err := store.ListPantryItems(ctx, householdID)
	if err != nil {
		return err
	}
	for _, pi := range pantry {
		if pi.ItemID != nil {
			continue
		}
		it, err := EnsureItem(ctx, store, householdID, pi.Name)
		if err != nil {
			return err
		}
		if it == nil {
			continue
		}
		id := it.ID
		if err := store.SetPantryItemItem(ctx, pi.ID, &id); err != nil {
			return err
		}
	}
	return nil
}
