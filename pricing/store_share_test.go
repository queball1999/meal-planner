package pricing

import (
	"context"
	"testing"
	"time"

	"goeat/db"
)

// delayedStoreProvider answers every store with a price equal to its store
// id, after that store's delay - so a test can make the primary store the
// slow one and check it still wins.
type delayedStoreProvider struct {
	delay map[int64]time.Duration
	miss  map[int64]bool
}

func (p *delayedStoreProvider) Name() string { return "delayed" }

func (p *delayedStoreProvider) Lookup(ctx context.Context, _ string, storeID int64, _ string) (*PriceResult, error) {
	select {
	case <-time.After(p.delay[storeID]):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.miss[storeID] {
		return nil, nil
	}
	return &PriceResult{PriceCents: storeID, PurchaseUnit: "each", PackSize: 1, Source: "manual", Confidence: ConfidenceManual}, nil
}

// TestResolveFirstStore_PrimaryShareWins: the store with the higher share
// wins even when a lower-share store answers first; with equal shares the
// first to answer still wins; and a primary that cannot price the item
// falls through to the next store instead of losing the line.
func TestResolveFirstStore_PrimaryShareWins(t *testing.T) {
	ctx := context.Background()
	primary := &db.GroceryStore{ID: 1, SharePct: 70}
	second := &db.GroceryStore{ID: 2, SharePct: 30}

	slowPrimary := &delayedStoreProvider{delay: map[int64]time.Duration{1: 40 * time.Millisecond}}
	_, sid, ok := resolveFirstStore(ctx, NewChain([]PriceProvider{slowPrimary}), "milk",
		[]*db.GroceryStore{primary, second}, "")
	if !ok || sid != 1 {
		t.Fatalf("slow primary: got store %d (ok=%v), want 1", sid, ok)
	}

	evenA := &db.GroceryStore{ID: 1}
	evenB := &db.GroceryStore{ID: 2}
	_, sid, ok = resolveFirstStore(ctx, NewChain([]PriceProvider{slowPrimary}), "milk",
		[]*db.GroceryStore{evenA, evenB}, "")
	if !ok || sid != 2 {
		t.Fatalf("equal shares: got store %d (ok=%v), want 2 (answered first)", sid, ok)
	}

	missPrimary := &delayedStoreProvider{miss: map[int64]bool{1: true}}
	_, sid, ok = resolveFirstStore(ctx, NewChain([]PriceProvider{missPrimary}), "milk",
		[]*db.GroceryStore{primary, second}, "")
	if !ok || sid != 2 {
		t.Fatalf("primary miss: got store %d (ok=%v), want 2", sid, ok)
	}
}

// TestCostPlan_ItemOnlyBoughtAtStore: an item tied to one store
// (items.preferred_store_id) is priced there even though another store has a
// higher share and a package for it too.
func TestCostPlan_ItemOnlyBoughtAtStore(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)

	big, _ := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "Big", Kind: "grocery"})
	small, _ := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "Small", Kind: "grocery"})
	if err := store.SetStoreShares(ctx, hh.ID, map[int64]int{big.ID: 80, small.ID: 20}); err != nil {
		t.Fatalf("shares: %v", err)
	}

	item, err := store.CreateItem(ctx, db.CreateItemParams{HouseholdID: hh.ID, Name: "coffee", NormalizedTerm: "coffee"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	for _, gs := range []*db.GroceryStore{big, small} {
		if err := store.UpsertItemStorePackage(ctx, db.UpsertItemStorePackageParams{
			ItemID: item.ID, StoreID: gs.ID, PurchaseUnit: "each", AmountPerPackage: 1, PriceCents: 500 + gs.ID,
		}); err != nil {
			t.Fatalf("package: %v", err)
		}
	}
	if err := store.SetStoreItems(ctx, hh.ID, small.ID, []int64{item.ID}); err != nil {
		t.Fatalf("store items: %v", err)
	}

	stores, _ := store.ListStores(ctx, hh.ID)
	if len(stores) != 2 || stores[0].ID != big.ID {
		t.Fatalf("ListStores should put the 80%% store first, got %+v", stores)
	}

	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 10000})
	meal, _ := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "breakfast", Title: "Coffee", Effort: "quick", Servings: 1, CookedPortions: 1})
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "coffee", Quantity: 1, Unit: "each", ItemID: &item.ID,
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	if _, err := CostPlan(ctx, store, NewChain(nil), p.ID, hh, stores); err != nil {
		t.Fatalf("cost plan: %v", err)
	}
	lines, _ := store.ListShoppingListItems(ctx, p.ID)
	if len(lines) != 1 || lines[0].StoreID == nil || *lines[0].StoreID != small.ID {
		t.Fatalf("coffee should be bought at Small only, got %+v", lines)
	}
}
