package web

import (
	"context"
	"testing"

	"goeat/db"
)

// TestItemIDForName: a typed "only buy here" name reuses the household's
// matching item and creates a manual one when nothing matches.
func TestItemIDForName(t *testing.T) {
	ctx := context.Background()
	s, hh := newSchedulerTestServer(t, 0)
	milk, err := s.store.CreateItem(ctx, db.CreateItemParams{HouseholdID: hh.ID, Name: "Milk", NormalizedTerm: "milk", StockUnit: "each", DefaultPurchaseQty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.itemIDForName(ctx, hh.ID, "  milk "); got != milk.ID {
		t.Errorf("existing name: got %d, want %d", got, milk.ID)
	}
	id := s.itemIDForName(ctx, hh.ID, "Oat bars")
	if id == 0 || id == milk.ID {
		t.Fatalf("new name: got %d", id)
	}
	if again := s.itemIDForName(ctx, hh.ID, "oat bars"); again != id {
		t.Errorf("second lookup created a duplicate: %d vs %d", again, id)
	}
	if got := s.itemIDForName(ctx, hh.ID, "   "); got != 0 {
		t.Errorf("blank name: got %d", got)
	}
}
