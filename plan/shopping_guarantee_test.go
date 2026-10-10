package plan

import (
	"context"
	"testing"
	"time"

	"goeat/db"
	"goeat/pricing"
)

// A plan is not usable without something to shop from. Pricing is optional
// (nil pricer here, as when no pricing chain is configured) and its failures
// are non-fatal, so Generate must fall back to an unpriced list rather than
// leaving the plan with nothing.
func TestGenerate_AlwaysWritesAShoppingList(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	items, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		t.Fatalf("list shopping items: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("plan finished with an empty shopping list")
	}
	for _, it := range items {
		if it.DisplayName == "" {
			t.Errorf("shopping line %d has no display name", it.ID)
		}
		if it.BuyQuantity <= 0 {
			t.Errorf("shopping line %q has buy quantity %v, want > 0", it.DisplayName, it.BuyQuantity)
		}
	}
}

// A slow pricer (a live scrape or AI lookup per ingredient in the real one)
// must not hold up Generate returning - it seeds skeleton rows and prices
// them in the background, so the caller (and the redirect to /plan it drives)
// sees the plan as ready long before pricing finishes.
func TestGenerate_PricingRunsInBackground(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	unblock := make(chan struct{})
	pricer := func(ctx context.Context, planID int64, hh *db.Household) error {
		<-unblock // stands in for a slow network-bound costing pass
		seeded, err := pricing.SeedShoppingList(ctx, store, planID, hh)
		if err != nil {
			return err
		}
		for _, s := range seeded {
			if err := store.UpdateShoppingListItemPrice(ctx, db.UpdateShoppingListItemPriceParams{
				ID: s.RowID, BuyQuantity: s.TotalQuantity, PackSize: s.TotalQuantity,
				PurchaseUnit: s.Unit, UnitPriceCents: 100, LineTotalCents: 100,
				PriceSource: pricing.ConfidenceEstimate, Confidence: pricing.ConfidenceEstimate,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	start := time.Now()
	planID, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, pricer, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("generate took %v - a blocked pricer should not delay it at all", elapsed)
	}

	p, err := store.GetPlanByID(ctx, planID)
	if err != nil || p == nil {
		t.Fatalf("get plan: %v", err)
	}
	if p.Status != "ready" {
		t.Fatalf("plan status = %q, want ready before pricing even started", p.Status)
	}
	if items, _ := store.ListShoppingListItems(ctx, planID); len(items) != 0 {
		t.Fatalf("got %d shopping list items before the pricer ran, want 0", len(items))
	}

	close(unblock)

	deadline := time.Now().Add(2 * time.Second)
	for {
		items, _ := store.ListShoppingListItems(ctx, planID)
		if len(items) > 0 {
			allResolved := true
			for _, it := range items {
				if it.Pending {
					allResolved = false
				}
			}
			if allResolved {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("background pricing did not finish resolving the list in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every day of the generated week gets an explicit headcount row, which is
// what per-day portion scaling keys off.
func TestGenerate_SeedsPlanDays(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	days, err := store.ListPlanDays(ctx, planID)
	if err != nil {
		t.Fatalf("list plan days: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("got %d plan days, want 7", len(days))
	}
	for _, d := range days {
		if d.Headcount != 2 { // the test household's size
			t.Errorf("%s headcount = %d, want 2", d.Date, d.Headcount)
		}
	}
}

// persistPlan must record the generated yield and quantities as the baseline
// that later headcount changes scale from.
func TestGenerate_RecordsScalingBaseline(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil || len(meals) == 0 {
		t.Fatalf("list meals: %v", err)
	}
	for _, m := range meals {
		if m.BaseServings != m.Servings {
			t.Fatalf("meal %d: base_servings %d != servings %d", m.ID, m.BaseServings, m.Servings)
		}
		if m.BaseCookedPortions != m.CookedPortions {
			t.Fatalf("meal %d: base_cooked_portions %d != cooked_portions %d", m.ID, m.BaseCookedPortions, m.CookedPortions)
		}
	}

	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil || len(ings) == 0 {
		t.Fatalf("list ingredients: %v", err)
	}
	for _, ing := range ings {
		if ing.BaseQuantity != ing.Quantity {
			t.Fatalf("ingredient %q: base_quantity %v != quantity %v", ing.Name, ing.BaseQuantity, ing.Quantity)
		}
	}
}
