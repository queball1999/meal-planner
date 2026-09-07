package pricing_test

import (
	"context"
	"math"
	"testing"

	"goeat/db"
	"goeat/pricing"
)

func newPantryStore(t *testing.T) (db.Store, int64) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{Name: "T"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh.ID
}

func stockPantry(t *testing.T, store db.Store, hhID int64, name, term string, qty float64, unit string, itemID *int64) {
	t.Helper()
	ctx := context.Background()
	pi, err := store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID: hhID, Name: name, NormalizedTerm: term,
		QuantityOnHand: qty, Unit: unit,
	})
	if err != nil {
		t.Fatalf("stock %s: %v", name, err)
	}
	if itemID != nil {
		if err := store.SetPantryItemItem(ctx, pi.ID, itemID); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
}

// The headline case: what the household already has is not bought again.
func TestApplyPantryReducesAndCovers(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)

	stockPantry(t, store, hhID, "Ground beef", "ground beef", 1, "lb", nil)
	stockPantry(t, store, hhID, "Olive oil", "olive oil", 5, "bottle", nil)

	items := []pricing.AggItem{
		{NormalizedTerm: "ground beef", DisplayName: "Ground beef", TotalQuantity: 3, Unit: "lb"},
		{NormalizedTerm: "olive oil", DisplayName: "Olive oil", TotalQuantity: 2, Unit: "bottle"},
		{NormalizedTerm: "saffron", DisplayName: "Saffron", TotalQuantity: 1, Unit: "g"},
	}

	got, err := pricing.ApplyPantry(ctx, store, hhID, items)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Partial cover: buy the difference.
	if math.Abs(items[0].TotalQuantity-2) > 1e-9 {
		t.Errorf("ground beef = %v, want 2 (3 needed - 1 on hand)", items[0].TotalQuantity)
	}
	if d := got[0]; d.Used != 1 || d.Covered {
		t.Errorf("ground beef deduction = %+v, want 1 used and not covered", d)
	}

	// Full cover: nothing to buy, but the line stays on the list.
	if items[1].TotalQuantity != 0 {
		t.Errorf("olive oil = %v, want 0", items[1].TotalQuantity)
	}
	if d := got[1]; d.Used != 2 || !d.Covered {
		t.Errorf("olive oil deduction = %+v, want 2 used and covered", d)
	}

	// Nothing on hand: untouched, and no deduction recorded.
	if items[2].TotalQuantity != 1 {
		t.Errorf("saffron = %v, want 1", items[2].TotalQuantity)
	}
	if _, ok := got[2]; ok {
		t.Error("saffron got a deduction with nothing in the pantry")
	}
}

// One pantry row cannot pay for two aggregates. Without per-row accounting the
// same jar covers both and the household ends the week with none.
func TestApplyPantryDoesNotDoubleSpend(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)
	stockPantry(t, store, hhID, "Butter", "butter", 3, "stick", nil)

	items := []pricing.AggItem{
		{NormalizedTerm: "butter", DisplayName: "Butter", TotalQuantity: 2, Unit: "stick"},
		{NormalizedTerm: "butter", DisplayName: "Butter", TotalQuantity: 2, Unit: "stick"},
	}
	got, _ := pricing.ApplyPantry(ctx, store, hhID, items)

	total := got[0].Used + got[1].Used
	if math.Abs(total-3) > 1e-9 {
		t.Errorf("spent %v sticks from a 3-stick pantry", total)
	}
	// The first aggregate takes 2, the second gets the remaining 1.
	if math.Abs(items[0].TotalQuantity-0) > 1e-9 {
		t.Errorf("first line = %v, want 0", items[0].TotalQuantity)
	}
	if math.Abs(items[1].TotalQuantity-1) > 1e-9 {
		t.Errorf("second line = %v, want 1", items[1].TotalQuantity)
	}
}

// Quantities that could not be unit-reconciled are approximate by
// construction. Subtracting a pantry amount from one is arithmetic on numbers
// in different units, and the failure - not buying what you need - is worse
// than the over-buying it would fix.
func TestApplyPantrySkipsUnconverted(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)
	stockPantry(t, store, hhID, "Flour", "flour", 10, "cup", nil)

	items := []pricing.AggItem{
		{NormalizedTerm: "flour", DisplayName: "Flour", TotalQuantity: 4, Unit: "cup", Unconverted: true},
	}
	got, _ := pricing.ApplyPantry(ctx, store, hhID, items)

	if items[0].TotalQuantity != 4 {
		t.Errorf("quantity = %v, want 4 untouched", items[0].TotalQuantity)
	}
	if _, ok := got[0]; ok {
		t.Error("an unconverted aggregate was deducted from")
	}
}

// A pantry row whose unit cannot be reconciled with the aggregate's is not a
// match, however similar the names.
func TestApplyPantrySkipsIncompatibleUnits(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)
	stockPantry(t, store, hhID, "Chicken", "chicken", 4, "each", nil)

	items := []pricing.AggItem{
		{NormalizedTerm: "chicken", DisplayName: "Chicken", TotalQuantity: 2, Unit: "lb"},
	}
	got, _ := pricing.ApplyPantry(ctx, store, hhID, items)

	if items[0].TotalQuantity != 2 {
		t.Errorf("quantity = %v, want 2 - 'each' is not convertible to lb", items[0].TotalQuantity)
	}
	if _, ok := got[0]; ok {
		t.Error("deducted across units that do not convert")
	}
}

// Matching by catalog item beats matching by name, and a pantry row linked to
// a different item is not a match at all - the link is the more specific fact.
func TestApplyPantryRespectsItemLinks(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)

	wanted, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hhID, Name: "Cheddar", NormalizedTerm: "cheddar",
		StockUnit: "g", DefaultPurchaseQty: 1, Source: "builtin",
	})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	other, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hhID, Name: "Cheddar (vegan)", NormalizedTerm: "cheddar vegan",
		StockUnit: "g", DefaultPurchaseQty: 1, Source: "builtin",
	})
	if err != nil {
		t.Fatalf("item: %v", err)
	}

	// A pantry row named the same but linked to the *other* item.
	stockPantry(t, store, hhID, "Cheddar", "cheddar", 500, "g", &other.ID)

	items := []pricing.AggItem{
		{NormalizedTerm: "cheddar", DisplayName: "Cheddar", TotalQuantity: 200, Unit: "g", ItemID: &wanted.ID},
	}
	got, _ := pricing.ApplyPantry(ctx, store, hhID, items)

	if items[0].TotalQuantity != 200 {
		t.Errorf("quantity = %v, want 200 - it matched a different catalog item", items[0].TotalQuantity)
	}
	if _, ok := got[0]; ok {
		t.Error("deducted from a pantry row linked to another item")
	}
}

func TestApplyPantryNoStockIsANoop(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)

	items := []pricing.AggItem{{NormalizedTerm: "beans", TotalQuantity: 2, Unit: "can"}}
	got, err := pricing.ApplyPantry(ctx, store, hhID, items)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(got) != 0 || items[0].TotalQuantity != 2 {
		t.Errorf("empty pantry changed something: %+v / %v", got, items[0].TotalQuantity)
	}

	// A zero-quantity row is stock the household does not have.
	stockPantry(t, store, hhID, "Beans", "beans", 0, "can", nil)
	got, _ = pricing.ApplyPantry(ctx, store, hhID, items)
	if len(got) != 0 || items[0].TotalQuantity != 2 {
		t.Errorf("a zero-quantity pantry row was treated as stock")
	}
}
