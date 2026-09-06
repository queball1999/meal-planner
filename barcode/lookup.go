// Package barcode resolves scanned UPC/EAN codes to app destinations (§8.4d).
package barcode

import (
	"context"
	"fmt"

	"goeat/db"
)

// Match is the resolved destination for a scanned barcode.
type Match struct {
	Kind string // "pantry" | "product"
	ID   int64
	URL  string // destination href the UI should navigate to
	Name string // human label for display
}

// Lookup resolves code against pantry_items and item_product_map.
// Resolution order (§8.4d):
//  1. pantry_items.barcode = code → pantry list (with item highlighted)
//  2. item_product_map.barcode = code → admin prices page
//
// Returns nil, nil when no match exists.
func Lookup(ctx context.Context, store db.Store, householdID int64, code string) (*Match, error) {
	// 1. Pantry item
	item, err := store.GetPantryItemByBarcode(ctx, householdID, code)
	if err != nil {
		return nil, fmt.Errorf("barcode: pantry lookup: %w", err)
	}
	if item != nil {
		return &Match{
			Kind: "pantry",
			ID:   item.ID,
			Name: item.Name,
			URL:  fmt.Sprintf("/pantry?q=%s", item.Name),
		}, nil
	}

	// 2. Item-product map
	pm, err := store.GetItemProductMapByBarcode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("barcode: product lookup: %w", err)
	}
	if pm != nil {
		return &Match{
			Kind: "product",
			ID:   pm.ID,
			Name: pm.ChosenProduct,
			URL:  "/admin/prices",
		}, nil
	}

	return nil, nil
}
