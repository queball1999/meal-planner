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
