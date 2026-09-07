package catalog_test

import (
	"context"
	"testing"

	"goeat/catalog"
	"goeat/db"
)

func newStore(t *testing.T) (db.Store, int64) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{Name: "Test"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh.ID
}

func TestSeedHousehold(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()

	if err := catalog.SeedHousehold(ctx, store, hhID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	items, err := store.ListItems(ctx, hhID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) < 50 {
		t.Fatalf("expected a substantial starter catalog, got %d items", len(items))
	}

	// A known item with an item-specific conversion bridge.
	var egg *db.Item
	for _, it := range items {
		if it.Name == "Large eggs" {
			egg = it
		}
	}
	if egg == nil {
		t.Fatalf("Large eggs not seeded")
	}
	if egg.Source != "builtin" {
		t.Fatalf("seeded item source = %q, want builtin", egg.Source)
	}
	conv, err := store.ListConversionsForItem(ctx, egg.ID)
	if err != nil {
		t.Fatalf("conversions: %v", err)
	}
	var haveEgg bool
	for _, c := range conv {
		if c.ItemID != nil && c.FromUnit == "each" && c.ToUnit == "g" {
			haveEgg = true
		}
	}
	if !haveEgg {
		t.Fatal("expected egg each->g bridge conversion")
	}

	// Auto-computed (derived) bridges are precomputed for a seeded item.
	var haveDerivedPkg bool
	for _, c := range conv {
		if c.Derived && c.FromUnit == "package" && c.ToUnit == egg.StockUnit {
			haveDerivedPkg = true
		}
	}
	if !haveDerivedPkg {
		t.Fatal("expected a derived package->stock-unit edge for Large eggs")
	}

	// Re-running only fills gaps, never duplicates.
	if err := catalog.SeedHousehold(ctx, store, hhID); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	again, _ := store.ListItems(ctx, hhID)
	if len(again) != len(items) {
		t.Fatalf("reseed changed item count %d -> %d", len(items), len(again))
	}
}

func TestEnsureItemIsIdempotent(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()

	a, err := catalog.EnsureItem(ctx, store, hhID, "Diced Yellow Onion")
	if err != nil || a == nil {
		t.Fatalf("ensure a: %v", err)
	}
	b, err := catalog.EnsureItem(ctx, store, hhID, "yellow onions")
	if err != nil || b == nil {
		t.Fatalf("ensure b: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("normalization mismatch: %d != %d (%q vs %q)", a.ID, b.ID, a.NormalizedTerm, b.NormalizedTerm)
	}
	if a.Source != "auto" {
		t.Fatalf("auto-created item source = %q, want auto", a.Source)
	}
}

func TestSeedGlobalConversions(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if err := catalog.SeedGlobalConversions(ctx, store); err != nil {
		t.Fatalf("seed globals: %v", err)
	}
	g, err := store.ListGlobalConversions(ctx)
	if err != nil {
		t.Fatalf("list globals: %v", err)
	}
	if len(g) < 10 {
		t.Fatalf("expected global conversions seeded, got %d", len(g))
	}
	// Idempotent.
	if err := catalog.SeedGlobalConversions(ctx, store); err != nil {
		t.Fatalf("reseed globals: %v", err)
	}
	g2, _ := store.ListGlobalConversions(ctx)
	if len(g2) != len(g) {
		t.Fatalf("reseed changed global count %d -> %d", len(g), len(g2))
	}
}

// EnsureItem's whole reason for growing past "look up or create": before
// aliases, every near-miss name silently became its own item with its own
// price and its own pantry stock.
func TestEnsureItemResolvesThroughAlias(t *testing.T) {
	ctx := context.Background()
	store, hhID := newStore(t)

	real, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hhID, Name: "Chicken breast", NormalizedTerm: "chicken breast",
		StockUnit: "lb", DefaultPurchaseQty: 1, Source: "builtin",
	})
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := store.CreateItemAlias(ctx, hhID, real.ID, "chix breast", "manual"); err != nil {
		t.Fatalf("alias: %v", err)
	}

	got, err := catalog.EnsureItem(ctx, store, hhID, "chix breast")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got == nil || got.ID != real.ID {
		t.Fatalf("resolved to %+v, want the aliased item %d", got, real.ID)
	}

	before, _ := store.ListItems(ctx, hhID)
	if _, err := catalog.EnsureItem(ctx, store, hhID, "chix breast"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	after, _ := store.ListItems(ctx, hhID)
	if len(after) != len(before) {
		t.Errorf("an aliased name created a new item: %d -> %d", len(before), len(after))
	}
}

// A confident fuzzy match is taken automatically and recorded as an alias, so
// the next sighting resolves without re-scoring the catalog.
//
// The example is a descriptive qualifier rather than a plural on purpose:
// pricing.Normalize already collapses plurals and prep words ("boneless
// skinless chicken breasts" -> "chicken breast"), so those never reach the
// fuzzy step. What it keeps - "extra virgin olive oil" normalizes to "virgin
// olive oil", not "olive oil" - is exactly what this step is for.
func TestEnsureItemAutoLinksCloseMatch(t *testing.T) {
	ctx := context.Background()
	store, hhID := newStore(t)

	real, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hhID, Name: "Olive oil", NormalizedTerm: "olive oil",
		StockUnit: "ml", DefaultPurchaseQty: 1, Source: "builtin",
	})
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}

	got, err := catalog.EnsureItem(ctx, store, hhID, "extra virgin olive oil")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got == nil || got.ID != real.ID {
		t.Fatalf("resolved to %+v, want the existing item %d - a near-duplicate was created", got, real.ID)
	}
	// Recorded, so the second lookup is an alias hit rather than a re-score of
	// the whole catalog.
	if a, _ := store.GetItemByAlias(ctx, hhID, "virgin olive oil"); a == nil || a.ID != real.ID {
		t.Errorf("auto match was not recorded as an alias: %+v", a)
	}
}

// A name that is genuinely new still gets its own placeholder - the shopping
// list flags those yellow rather than guessing.
func TestEnsureItemStillCreatesForUnrelatedNames(t *testing.T) {
	ctx := context.Background()
	store, hhID := newStore(t)

	if _, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hhID, Name: "Chicken breast", NormalizedTerm: "chicken breast",
		StockUnit: "lb", DefaultPurchaseQty: 1, Source: "builtin",
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	got, err := catalog.EnsureItem(ctx, store, hhID, "pomegranate molasses")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got == nil {
		t.Fatal("no item created for an unrelated name")
	}
	if got.Source != "auto" {
		t.Errorf("source = %q, want auto - this is the yellow-chip case", got.Source)
	}
}
