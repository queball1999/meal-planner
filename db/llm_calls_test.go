package db_test

import (
	"context"
	"fmt"
	"testing"

	"goeat/db"
)

// The Audit Log reads newest first and keeps only the newest LLMCallKeep rows,
// so a long plan run trims the oldest calls, never the ones just made.
func TestLLMCallsNewestFirstAndPruned(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	total := db.LLMCallKeep + 5
	for i := 1; i <= total; i++ {
		if err := store.InsertLLMCall(ctx, db.LLMCall{
			HouseholdID: hh.ID, Purpose: "plan", Prompt: fmt.Sprintf("p%d", i),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	all, err := store.ListLLMCalls(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != db.LLMCallKeep {
		t.Fatalf("kept %d rows, want %d", len(all), db.LLMCallKeep)
	}
	if all[0].Prompt != fmt.Sprintf("p%d", total) || all[len(all)-1].Prompt != "p6" {
		t.Errorf("order/prune wrong: first=%q last=%q", all[0].Prompt, all[len(all)-1].Prompt)
	}
	if all[0].HouseholdID != hh.ID || all[0].Purpose != "plan" || all[0].At.IsZero() {
		t.Errorf("fields did not round-trip: %+v", all[0])
	}

	top, _ := store.ListLLMCalls(ctx, 3)
	if len(top) != 3 || top[0].Prompt != all[0].Prompt {
		t.Errorf("limited list = %d rows, want the 3 newest", len(top))
	}
}

// A call made outside a household (settings probe, background job) is still
// logged.
func TestLLMCallWithoutHousehold(t *testing.T) {
	ctx := context.Background()
	store, _ := newPlansTestStore(t)
	if err := store.InsertLLMCall(ctx, db.LLMCall{Prompt: "x", Error: "boom"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := store.ListLLMCalls(ctx, 1)
	if len(got) != 1 || got[0].HouseholdID != 0 || got[0].Error != "boom" {
		t.Errorf("got %+v", got)
	}
}

// The generation screen's debug panel asks for one plan's calls: it gets
// those alone, newest first, and never another household's.
func TestLLMCallsForPlan(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	for _, c := range []db.LLMCall{
		{HouseholdID: hh.ID, PlanID: 4, Purpose: "plan", Prompt: "first"},
		{HouseholdID: hh.ID, Purpose: "price_estimate", Prompt: "other plan's pricing"},
		{HouseholdID: hh.ID, PlanID: 3, Purpose: "plan", Prompt: "older plan"},
		{HouseholdID: hh.ID, PlanID: 4, Purpose: "scrape", Prompt: "second"},
	} {
		if err := store.InsertLLMCall(ctx, c); err != nil {
			t.Fatalf("insert %q: %v", c.Prompt, err)
		}
	}

	got, err := store.ListLLMCallsForPlan(ctx, hh.ID, 4)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Prompt != "second" || got[1].Prompt != "first" || got[0].PlanID != 4 {
		t.Fatalf("plan 4 calls = %+v", got)
	}
	if other, _ := store.ListLLMCallsForPlan(ctx, hh.ID+1, 4); len(other) != 0 {
		t.Errorf("another household read %d of this plan's calls", len(other))
	}
	// Scoping the panel takes nothing away from the Audit Log.
	if all, _ := store.ListLLMCalls(ctx, 0); len(all) != 4 {
		t.Errorf("audit log has %d calls, want all 4", len(all))
	}
}
