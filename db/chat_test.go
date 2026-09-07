package db_test

import (
	"context"
	"testing"

	"goeat/db"
)

// A conversation is read oldest-first, but what has to be kept when it grows
// long is the recent end.
func TestChatMessagesOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	for _, m := range []struct{ role, content string }{
		{"user", "one"}, {"assistant", "two"}, {"user", "three"}, {"assistant", "four"},
	} {
		if _, err := store.AppendChatMessage(ctx, hh.ID, m.role, m.content, ""); err != nil {
			t.Fatalf("append %s: %v", m.content, err)
		}
	}

	all, err := store.ListChatMessages(ctx, hh.ID, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 4 || all[0].Content != "one" || all[3].Content != "four" {
		t.Fatalf("order wrong: %+v", contents(all))
	}

	// A limit keeps the newest, still in chronological order.
	recent, err := store.ListChatMessages(ctx, hh.ID, 2)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(recent) != 2 || recent[0].Content != "three" || recent[1].Content != "four" {
		t.Errorf("limited list = %v, want [three four]", contents(recent))
	}
}

func contents(ms []*db.ChatMessage) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Content
	}
	return out
}

func TestChatAuditDefaultsToEmptyArray(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	if _, err := store.AppendChatMessage(ctx, hh.ID, "user", "hi", ""); err != nil {
		t.Fatalf("append: %v", err)
	}
	msgs, _ := store.ListChatMessages(ctx, hh.ID, 10)
	if len(msgs) != 1 || msgs[0].AuditJSON != "[]" {
		t.Errorf("audit = %q, want [] - the column is NOT NULL and the UI unmarshals it", msgs[0].AuditJSON)
	}
}

func TestClearChatMessagesIsScopedToHousehold(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Other"})
	if err != nil {
		t.Fatalf("other household: %v", err)
	}

	if _, err := store.AppendChatMessage(ctx, hh.ID, "user", "mine", ""); err != nil {
		t.Fatalf("append mine: %v", err)
	}
	if _, err := store.AppendChatMessage(ctx, other.ID, "user", "theirs", ""); err != nil {
		t.Fatalf("append theirs: %v", err)
	}

	if err := store.ClearChatMessages(ctx, hh.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if mine, _ := store.ListChatMessages(ctx, hh.ID, 10); len(mine) != 0 {
		t.Errorf("own conversation survived the clear: %v", contents(mine))
	}
	if theirs, _ := store.ListChatMessages(ctx, other.ID, 10); len(theirs) != 1 {
		t.Error("another household's conversation was cleared")
	}
}
