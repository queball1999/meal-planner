package db_test

import (
	"context"
	"math"
	"testing"

	"goeat/db"
)

func newItem(t *testing.T, store db.Store, householdID int64, name, term, source string) *db.Item {
	t.Helper()
	it, err := store.CreateItem(context.Background(), db.CreateItemParams{
		HouseholdID:        householdID,
		Name:               name,
		NormalizedTerm:     term,
		StockUnit:          "each",
		DefaultPurchaseQty: 1,
		Source:             source,
	})
	if err != nil {
		t.Fatalf("create item %s: %v", name, err)
	}
	return it
}

func TestItemAliasRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	item := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")

	if err := store.CreateItemAlias(ctx, hh.ID, item.ID, "chicken breasts", "manual"); err != nil {
		t.Fatalf("create alias: %v", err)
	}
	got, err := store.GetItemByAlias(ctx, hh.ID, "chicken breasts")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || got.ID != item.ID {
		t.Fatalf("alias resolved to %+v, want item %d", got, item.ID)
	}

	if miss, _ := store.GetItemByAlias(ctx, hh.ID, "nothing like it"); miss != nil {
		t.Error("an unknown alias resolved to something")
	}

	aliases, _ := store.ListItemAliases(ctx, hh.ID, item.ID)
	if len(aliases) != 1 || aliases[0].Source != "manual" {
		t.Errorf("aliases = %+v, want one manual entry", aliases)
	}

	if err := store.DeleteItemAlias(ctx, hh.ID, aliases[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gone, _ := store.GetItemByAlias(ctx, hh.ID, "chicken breasts"); gone != nil {
		t.Error("alias survived deletion")
	}
}

// The match dialog exists so a person can correct a bad guess, so repointing
// an existing alias has to work rather than fail on the unique constraint.
func TestItemAliasRepoints(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	wrong := newItem(t, store, hh.ID, "Chicken thigh", "chicken thigh", "builtin")
	right := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")

	if err := store.CreateItemAlias(ctx, hh.ID, wrong.ID, "chix", "auto"); err != nil {
		t.Fatalf("first alias: %v", err)
	}
	if err := store.CreateItemAlias(ctx, hh.ID, right.ID, "chix", "manual"); err != nil {
		t.Fatalf("repoint: %v", err)
	}

	got, _ := store.GetItemByAlias(ctx, hh.ID, "chix")
	if got == nil || got.ID != right.ID {
		t.Errorf("alias points at %+v, want item %d", got, right.ID)
	}
}

func TestMergeItemsMovesReferencesAndAliases(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	keep := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")
	drop := newItem(t, store, hh.ID, "chicken breasts", "chicken breasts", "auto")

	// The placeholder has picked up an alias of its own along the way.
	if err := store.CreateItemAlias(ctx, hh.ID, drop.ID, "chix", "auto"); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	if err := store.MergeItems(ctx, hh.ID, drop.ID, keep.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if gone, _ := store.GetItem(ctx, drop.ID); gone != nil {
		t.Error("the merged item still exists")
	}
	// The loser's own term becomes an alias, so the name that created it
	// resolves correctly next time instead of creating it again.
	if got, _ := store.GetItemByAlias(ctx, hh.ID, "chicken breasts"); got == nil || got.ID != keep.ID {
		t.Errorf("merged term does not resolve to the survivor: %+v", got)
	}
	// An alias that pointed at the loser follows it.
	if got, _ := store.GetItemByAlias(ctx, hh.ID, "chix"); got == nil || got.ID != keep.ID {
		t.Errorf("carried alias does not resolve to the survivor: %+v", got)
	}
}

// Pantry rows are unique per normalized term, so two rows cannot simply both
// point at the survivor - the quantities are added, since the household does
// own both lots.
func TestMergeItemsCombinesPantryQuantities(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	keep := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")
	drop := newItem(t, store, hh.ID, "chicken breasts", "chicken breasts", "auto")

	keepPantry, err := store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID: hh.ID, Name: "Chicken breast", NormalizedTerm: "chicken breast",
		QuantityOnHand: 2, Unit: "lb",
	})
	if err != nil {
		t.Fatalf("pantry keep: %v", err)
	}
	dropPantry, err := store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID: hh.ID, Name: "chicken breasts", NormalizedTerm: "chicken breasts",
		QuantityOnHand: 3, Unit: "lb",
	})
	if err != nil {
		t.Fatalf("pantry drop: %v", err)
	}
	if err := store.SetPantryItemItem(ctx, keepPantry.ID, &keep.ID); err != nil {
		t.Fatalf("link keep: %v", err)
	}
	if err := store.SetPantryItemItem(ctx, dropPantry.ID, &drop.ID); err != nil {
		t.Fatalf("link drop: %v", err)
	}

	if err := store.MergeItems(ctx, hh.ID, drop.ID, keep.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	rows, _ := store.ListPantryItems(ctx, hh.ID)
	var total float64
	var linked int
	for _, r := range rows {
		if r.ItemID != nil && *r.ItemID == keep.ID {
			linked++
			total += r.QuantityOnHand
		}
	}
	if linked != 1 {
		t.Errorf("%d pantry rows point at the survivor, want 1", linked)
	}
	if math.Abs(total-5) > 1e-9 {
		t.Errorf("combined quantity = %v, want 5", total)
	}
}

// An item id from another household must not be mergeable by guessing it.
func TestMergeItemsRejectsForeignItem(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	mine := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")

	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Other"})
	if err != nil {
		t.Fatalf("other household: %v", err)
	}
	theirs := newItem(t, store, other.ID, "Their chicken", "their chicken", "builtin")

	if err := store.MergeItems(ctx, hh.ID, theirs.ID, mine.ID); err == nil {
		t.Error("merged another household's item")
	}
	if still, _ := store.GetItem(ctx, theirs.ID); still == nil {
		t.Error("the other household's item was deleted anyway")
	}
}

func TestMergeItemsIntoItselfIsANoop(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	item := newItem(t, store, hh.ID, "Chicken breast", "chicken breast", "builtin")

	if err := store.MergeItems(ctx, hh.ID, item.ID, item.ID); err != nil {
		t.Fatalf("self-merge: %v", err)
	}
	if gone, _ := store.GetItem(ctx, item.ID); gone == nil {
		t.Error("a self-merge deleted the item")
	}
}
