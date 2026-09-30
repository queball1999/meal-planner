package catalog_test

import (
	"context"
	"testing"

	"goeat/catalog"
	"goeat/pricing"
)

// A fresh tomato counted "each" is not the starter catalog's can of diced
// tomatoes, while black beans or tuna bought by the can still find their
// canned item.
func TestEnsureItemKeepsCannedApart(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()
	if err := catalog.SeedHousehold(ctx, store, hhID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tomato, err := catalog.EnsureItemWithHint(ctx, store, hhID, "tomato", catalog.ItemHint{Unit: "each"})
	if err != nil || tomato == nil {
		t.Fatalf("tomato: %v %v", tomato, err)
	}
	if tomato.NormalizedTerm == "canned tomato" {
		t.Errorf("fresh tomato linked to %q", tomato.Name)
	}

	for _, c := range []struct{ name, unit, want string }{
		{"black beans", "can", "canned black bean"},
		{"canned black beans", "", "canned black bean"},
		{"tuna", "oz", "canned tuna"},
	} {
		it, err := catalog.EnsureItemWithHint(ctx, store, hhID, c.name, catalog.ItemHint{Unit: c.unit})
		if err != nil || it == nil {
			t.Fatalf("%s: %v %v", c.name, it, err)
		}
		if it.NormalizedTerm != c.want {
			t.Errorf("%s (%s) -> %q, want %q", c.name, c.unit, it.NormalizedTerm, c.want)
		}
	}
}

// A new item the generator says is sold by the package is still counted in
// each: "6 tortillas", not "0.6 packages".
func TestEnsureItemStocksContainersByEach(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()
	it, err := catalog.EnsureItemWithHint(ctx, store, hhID, "flour tortillas", catalog.ItemHint{
		Unit:        "package",
		Conversions: []catalog.UnitEdge{{From: "each", To: "package", Factor: 0.1}},
	})
	if err != nil || it == nil {
		t.Fatalf("ensure: %v %v", it, err)
	}
	if it.StockUnit != "each" {
		t.Errorf("stock unit = %q, want each", it.StockUnit)
	}
	conv, _ := store.ListConversionsForItem(ctx, it.ID)
	if got, ok := pricing.Convert(1, "package", "each", conv); !ok || got != 10 {
		t.Errorf("1 package = %v each (ok=%v), want 10", got, ok)
	}
}
