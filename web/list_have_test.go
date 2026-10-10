package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"goeat/db"
	"goeat/pricing"
)

// Saying "I have 3" of a line that needs 23 is not having it: the line stays
// on the list to buy the other 20, and the pantry is told about the 3.
func TestListHavePartialKeepsLineToBuy(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	planID := f.a.meal.PlanID
	if err := f.store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: f.a.meal.ID, Name: "eggs", Quantity: 23, Unit: "each", NormalizedTerm: pricing.Normalize("eggs"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pricing.SeedShoppingList(ctx, f.store, planID, f.a.hh); err != nil {
		t.Fatal(err)
	}
	eggs := func() *db.ShoppingListItem {
		t.Helper()
		rows, _ := f.store.ListShoppingListItems(ctx, planID)
		if len(rows) != 1 {
			t.Fatalf("got %d lines, want 1", len(rows))
		}
		return rows[0]
	}

	w := f.do("alice", "POST", fmt.Sprintf("/list/%d/have", eggs().ID),
		url.Values{"have": {"1"}, "quantity": {"3"}, "unit": {"each"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"partial":true`, `"have":false`, "20 still to buy"} {
		if !strings.Contains(body, want) {
			t.Errorf("response is missing %q: %s", want, body)
		}
	}

	// The rescale runs in the background; let it finish before reading the
	// line (and before the fixture closes the store under it).
	for i := 0; f.srv.isRepricing(planID); i++ {
		if i > 500 {
			t.Fatal("rescale did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	row := eggs()
	if row.InPantry || row.PantryQtyUsed != 3 || row.BuyQuantity != 20 {
		t.Errorf("line: in_pantry=%v used=%v buy=%v, want false/3/20", row.InPantry, row.PantryQtyUsed, row.BuyQuantity)
	}
	if note := pantryNote(row); !strings.Contains(note, "20 to buy") {
		t.Errorf("pantry note = %q, want it to say 20 to buy", note)
	}
	pi, _ := f.store.GetPantryItemByTerm(ctx, f.a.hh.ID, pricing.Normalize("eggs"))
	if pi == nil || pi.QuantityOnHand != 3 {
		t.Errorf("pantry row = %+v, want 3 eggs", pi)
	}
}
