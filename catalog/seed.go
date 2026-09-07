package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"goeat/db"
	"goeat/pricing"
)

//go:embed seed_items.json
var seedItemsJSON []byte

type seedConversion struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Factor float64 `json:"factor"`
}

type seedItem struct {
	Name               string           `json:"name"`
	Category           string           `json:"category"`
	StockUnit          string           `json:"stock_unit"`
	DefaultPurchaseQty float64          `json:"default_purchase_qty"`
	ImageSourceURL     string           `json:"image_source_url,omitempty"`
	ImageAttribution   string           `json:"image_attribution,omitempty"`
	Conversions        []seedConversion `json:"conversions,omitempty"`
}

type seedFile struct {
	Items []seedItem `json:"items"`
}

// globalConversions are the metric/imperial edges seeded into unit_conversions
// with a NULL item_id, so the conversions editor and any DB-graph consumer see
// them. pricing.Convert also carries these as a code fallback.
var globalConversions = []seedConversion{
	{"g", "kg", 0.001},
	{"kg", "lb", 2.2046226218},
	{"g", "oz", 0.0352739619},
	{"lb", "oz", 16},
	{"lb", "g", 453.59237},
	{"ml", "l", 0.001},
	{"l", "cup", 4.2267528377},
	{"ml", "tsp", 0.2028841362},
	{"tbsp", "tsp", 3},
	{"cup", "tbsp", 16},
	{"fl-oz", "tbsp", 2},
	{"cup", "fl-oz", 8},
	{"pint", "cup", 2},
	{"quart", "pint", 2},
	{"gallon", "quart", 4},
	{"dozen", "each", 12},
}

// SeedGlobalConversions writes the global metric/imperial conversion edges.
// Idempotent - UpsertUnitConversion replaces on (NULL, from, to).
func SeedGlobalConversions(ctx context.Context, store db.Store) error {
	for _, c := range globalConversions {
		if err := store.UpsertUnitConversion(ctx, db.UpsertUnitConversionParams{
			FromUnit: c.From, ToUnit: c.To, Factor: c.Factor,
		}); err != nil {
			return fmt.Errorf("seed global conversion %s->%s: %w", c.From, c.To, err)
		}
	}
	return nil
}

// SeedHousehold inserts the starter catalog (source='builtin') for a household
// and its per-item conversion bridges. Items whose normalized term already
// exists are skipped, so a re-run only fills gaps.
func SeedHousehold(ctx context.Context, store db.Store, householdID int64) error {
	var sf seedFile
	if err := json.Unmarshal(seedItemsJSON, &sf); err != nil {
		return fmt.Errorf("parse seed_items.json: %w", err)
	}

	existing, err := store.ListItems(ctx, householdID)
	if err != nil {
		return err
	}
	haveTerm := make(map[string]bool, len(existing))
	for _, it := range existing {
		haveTerm[it.NormalizedTerm] = true
	}

	for _, si := range sf.Items {
		term := pricing.Normalize(si.Name)
		if term == "" || haveTerm[term] {
			continue
		}
		unit := si.StockUnit
		if unit == "" {
			unit = "each"
		}
		qty := si.DefaultPurchaseQty
		if qty == 0 {
			qty = 1
		}
		it, err := store.CreateItem(ctx, db.CreateItemParams{
			HouseholdID:        householdID,
			Name:               si.Name,
			NormalizedTerm:     term,
			Category:           si.Category,
			StockUnit:          unit,
			DefaultPurchaseQty: qty,
			ImageSourceURL:     si.ImageSourceURL,
			ImageAttribution:   si.ImageAttribution,
			Source:             "builtin",
		})
		if err != nil {
			return fmt.Errorf("seed item %q: %w", si.Name, err)
		}
		haveTerm[term] = true
		for _, c := range si.Conversions {
			id := it.ID
			if err := store.UpsertUnitConversion(ctx, db.UpsertUnitConversionParams{
				ItemID: &id, FromUnit: c.From, ToUnit: c.To, Factor: c.Factor,
			}); err != nil {
				return fmt.Errorf("seed conversion for %q: %w", si.Name, err)
			}
		}
		if err := RecalcItemConversions(ctx, store, it.ID); err != nil {
			return fmt.Errorf("recalc conversions for %q: %w", si.Name, err)
		}
	}
	return nil
}
