package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"goeat/db"
	"goeat/settings"
)

// fakeHA is a minimal in-memory Home Assistant to-do API.
type fakeHA struct {
	mu     sync.Mutex
	items  []TodoItem
	nextID int
}

func (f *fakeHA) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":"API running."}`))
	})
	mux.HandleFunc("/api/services/todo/add_item", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Item string `json:"item"` }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.nextID++
		f.items = append(f.items, TodoItem{UID: fmt.Sprintf("u%d", f.nextID), Summary: b.Item, Status: "needs_action"})
		f.mu.Unlock()
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/services/todo/update_item", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Item, Status string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		for i := range f.items {
			if f.items[i].UID == b.Item || strings.EqualFold(f.items[i].Summary, b.Item) {
				if b.Status != "" {
					f.items[i].Status = b.Status
				}
			}
		}
		f.mu.Unlock()
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/services/todo/remove_item", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Item string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		out := f.items[:0]
		for _, it := range f.items {
			if it.UID != b.Item && !strings.EqualFold(it.Summary, b.Item) {
				out = append(out, it)
			}
		}
		f.items = out
		f.mu.Unlock()
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/services/todo/get_items", func(w http.ResponseWriter, r *http.Request) {
		// Real Home Assistant defaults "status" to ["needs_action"] only when
		// the caller doesn't pass it explicitly - mirror that here so a test
		// relying on GetItems() also seeing completed items actually exercises
		// the fix (client.go must send status explicitly) instead of passing
		// by accident against a fake that always returns everything.
		var b struct {
			Status []string `json:"status"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		statuses := b.Status
		if len(statuses) == 0 {
			statuses = []string{"needs_action"}
		}
		want := map[string]bool{}
		for _, s := range statuses {
			want[s] = true
		}
		f.mu.Lock()
		var items []TodoItem
		for _, it := range f.items {
			if want[it.Status] {
				items = append(items, it)
			}
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"service_response": map[string]any{
				"todo.shopping_list": map[string]any{"items": items},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeHA) complete(summary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.items {
		if strings.EqualFold(f.items[i].Summary, summary) {
			f.items[i].Status = "completed"
		}
	}
}

func newStore(t *testing.T) (db.Store, *db.Household) {
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
	return store, hh
}

func seedPlanWithItems(t *testing.T, store db.Store, hh *db.Household, names ...string) *db.Plan {
	t.Helper()
	ctx := context.Background()
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, n := range names {
		if _, err := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID: p.ID, DisplayName: n, BuyQuantity: 2, PurchaseUnit: "lb",
			PriceSource: "estimate", Confidence: "estimate",
		}); err != nil {
			t.Fatalf("sl item: %v", err)
		}
	}
	return p
}

func hcFor(base string) settings.HAConfig {
	return settings.HAConfig{BaseURL: base, Token: "tok", TodoEntity: "todo.shopping_list", ItemFormat: "name_qty", IntervalMinutes: 5}
}

func TestPushList_AddsMissingItems(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	seedPlanWithItems(t, store, hh, "chicken thighs", "rice")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")

	res, err := PushList(ctx, store, client, hcFor(srv.URL), hh)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Pushed != 2 {
		t.Fatalf("pushed = %d, want 2", res.Pushed)
	}
	if len(ha.items) != 2 {
		t.Fatalf("HA has %d items, want 2", len(ha.items))
	}
	if !strings.Contains(ha.items[0].Summary, "chicken thighs") {
		t.Fatalf("unexpected summary %q", ha.items[0].Summary)
	}

	// Idempotent: a second push adds nothing.
	res2, _ := PushList(ctx, store, client, hcFor(srv.URL), hh)
	if res2.Pushed != 0 || len(ha.items) != 2 {
		t.Fatalf("second push not idempotent: pushed=%d items=%d", res2.Pushed, len(ha.items))
	}
}

func TestPullList_MarksLocalDoneWhenCompletedInHA(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	p := seedPlanWithItems(t, store, hh, "chicken thighs", "rice")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")
	hc := hcFor(srv.URL)

	if _, err := PushList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("push: %v", err)
	}
	// User checks "chicken thighs" off in Home Assistant.
	ha.complete("2 lb chicken thighs")

	res, err := PullList(ctx, store, client, hc, hh)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.Checked != 1 {
		t.Fatalf("checked = %d, want 1", res.Checked)
	}
	lines, _ := store.ListShoppingListItems(ctx, p.ID)
	var chickenChecked, riceChecked bool
	for _, ln := range lines {
		if strings.Contains(ln.DisplayName, "chicken") {
			chickenChecked = ln.Checked
		}
		if strings.Contains(ln.DisplayName, "rice") {
			riceChecked = ln.Checked
		}
	}
	if !chickenChecked {
		t.Fatal("chicken should be checked locally after HA completion")
	}
	if riceChecked {
		t.Fatal("rice should still be unchecked")
	}
}

func TestPushList_RemovesDroppedItems(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	seedPlanWithItems(t, store, hh, "chicken thighs", "rice")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")
	hc := hcFor(srv.URL)

	PushList(ctx, store, client, hc, hh)
	if len(ha.items) != 2 {
		t.Fatalf("setup: HA has %d", len(ha.items))
	}

	// New plan without "rice".
	seedPlanWithItems(t, store, hh, "chicken thighs")
	res, err := PushList(ctx, store, client, hc, hh)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("removed = %d, want 1", res.Removed)
	}
	if len(ha.items) != 1 || !strings.Contains(ha.items[0].Summary, "chicken") {
		t.Fatalf("HA list wrong after drop: %+v", ha.items)
	}
}

// TestPullList_LocalUncheckSurvivesStaleHACompletedStatus reproduces the bug
// report: an item checked off on both sides, then unchecked locally in Go Eat.
// Before PushList gets a chance to tell HA about the uncheck, a scheduler tick
// (or a second manual sync) can run PullList against HA, which still shows
// "completed". Pull must not blindly trust that stale status and re-check the
// item locally - it should only do that when nothing unchecked it locally
// since the last sync (row.LocalChecked).
func TestPullList_LocalUncheckSurvivesStaleHACompletedStatus(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	p := seedPlanWithItems(t, store, hh, "butter")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")
	hc := hcFor(srv.URL)

	// Get to "completed on both sides".
	if _, err := PushList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("push: %v", err)
	}
	ha.complete("2 lb butter")
	if _, err := PullList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("pull: %v", err)
	}
	lines, _ := store.ListShoppingListItems(ctx, p.ID)
	if !lines[0].Checked {
		t.Fatal("setup: butter should be checked after HA completion pulled in")
	}

	// User unchecks butter directly in Go Eat. HA still says "completed" -
	// PushList hasn't run yet to tell it otherwise.
	if err := store.CheckShoppingListItem(ctx, lines[0].ID, false); err != nil {
		t.Fatalf("uncheck: %v", err)
	}

	// A sync tick runs Pull before Push, same as SyncOnce.
	res, err := PullList(ctx, store, client, hc, hh)
	if err != nil {
		t.Fatalf("pull 2: %v", err)
	}
	if res.Checked != 0 {
		t.Fatalf("pull re-checked the item it should have left alone: %+v", res)
	}
	lines, _ = store.ListShoppingListItems(ctx, p.ID)
	if lines[0].Checked {
		t.Fatal("local uncheck was overridden by stale HA status")
	}
}

// TestSyncOnce_UncheckReappearsInHA is the end-to-end version of the bug
// report: uncheck an item that's completed on both sides, run a full sync
// (pull then push, as SyncOnce and the scheduler do), and confirm HA ends up
// showing the item again as needs_action rather than it staying completed (or
// vanishing from a "needs_action only" list view).
func TestSyncOnce_UncheckReappearsInHA(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	p := seedPlanWithItems(t, store, hh, "butter")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")
	hc := hcFor(srv.URL)

	if _, err := PushList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("push: %v", err)
	}
	ha.complete("2 lb butter")
	if _, err := PullList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("pull: %v", err)
	}

	lines, _ := store.ListShoppingListItems(ctx, p.ID)
	if err := store.CheckShoppingListItem(ctx, lines[0].ID, false); err != nil {
		t.Fatalf("uncheck: %v", err)
	}

	if _, err := PullList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("pull 2: %v", err)
	}
	if _, err := PushList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("push 2: %v", err)
	}

	if len(ha.items) != 1 {
		t.Fatalf("HA has %d items, want 1", len(ha.items))
	}
	if ha.items[0].Status != "needs_action" {
		t.Fatalf("HA status = %q, want needs_action", ha.items[0].Status)
	}

	lines, _ = store.ListShoppingListItems(ctx, p.ID)
	if lines[0].Checked {
		t.Fatal("butter should still be unchecked locally after the full sync")
	}
}

// TestPushList_ReAddsAfterHAClearedTheList reproduces: the user deletes every
// item directly in the Home Assistant to-do list (or the integration purges
// them), then hits "Sync" in Go Eat. Before the knownUID check, PushList saw a
// non-empty (but now stale) row.HAUID and skipped the AddItem path entirely,
// then failed the follow-up update_item call against a uid HA no longer has -
// so nothing ever came back. It must re-add instead.
func TestPushList_ReAddsAfterHAClearedTheList(t *testing.T) {
	ctx := context.Background()
	store, hh := newStore(t)
	seedPlanWithItems(t, store, hh, "chicken thighs", "rice")
	ha := &fakeHA{}
	srv := ha.server(t)
	client := NewClient(srv.URL, "tok")
	hc := hcFor(srv.URL)

	if _, err := PushList(ctx, store, client, hc, hh); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(ha.items) != 2 {
		t.Fatalf("setup: HA has %d items, want 2", len(ha.items))
	}

	// User clears the HA to-do list directly - Go Eat's ha_sync_map rows still
	// carry the now-dead uids.
	ha.mu.Lock()
	ha.items = nil
	ha.mu.Unlock()

	res, err := PushList(ctx, store, client, hc, hh)
	if err != nil {
		t.Fatalf("push after clear: %v", err)
	}
	if res.Pushed != 2 {
		t.Fatalf("pushed = %d, want 2 (both items should be re-added)", res.Pushed)
	}
	if len(ha.items) != 2 {
		t.Fatalf("HA has %d items after re-sync, want 2", len(ha.items))
	}
}

func TestPing(t *testing.T) {
	ha := &fakeHA{}
	srv := ha.server(t)
	if err := NewClient(srv.URL, "tok").Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
