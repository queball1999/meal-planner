package homeassistant

import (
	"context"
	"fmt"
	"log"
	"strings"

	"goeat/db"
	"goeat/pricing"
	"goeat/settings"
)

// Result summarizes one sync pass.
type Result struct {
	Pushed       int
	Removed      int
	Checked      int // local items marked done from HA
	Unchecked    int
	CheckedIDs   []int64 // shopping_list_item IDs newly marked done, for the UI to update in place
	UncheckedIDs []int64 // shopping_list_item IDs newly marked not-done
}

// termFor returns the normalized grocery term for a shopping-list line.
func termFor(ctx context.Context, store db.Store, it *db.ShoppingListItem) string {
	if it.ItemID != nil {
		if item, _ := store.GetItem(ctx, *it.ItemID); item != nil && item.NormalizedTerm != "" {
			return item.NormalizedTerm
		}
	}
	return pricing.Normalize(it.DisplayName)
}

// label renders the text pushed to HA for a shopping-list line.
func label(it *db.ShoppingListItem, format string) string {
	name := strings.TrimSpace(it.DisplayName)
	if format != "name_qty" || it.BuyQuantity <= 0 {
		return name
	}
	qty := pricing.FormatQty(it.BuyQuantity)
	unit := strings.TrimSpace(it.PurchaseUnit)
	if unit == "" || unit == "each" {
		return fmt.Sprintf("%s %s", qty, name)
	}
	return fmt.Sprintf("%s %s %s", qty, unit, name)
}

// PushList sends the household's current shopping list to Home Assistant:
// adds missing items, mirrors the checked state, and removes lines that have
// dropped off the plan. The app owns list membership; HA owns completion.
func PushList(ctx context.Context, store db.Store, client *Client, hc settings.HAConfig, hh *db.Household) (Result, error) {
	var res Result
	plan, err := store.GetLatestPlan(ctx, hh.ID)
	if err != nil {
		return res, err
	}
	if plan == nil {
		return res, nil
	}
	lines, err := store.ListShoppingListItems(ctx, plan.ID)
	if err != nil {
		return res, err
	}
	haItems, err := client.GetItems(ctx, hc.TodoEntity)
	if err != nil {
		return res, err
	}
	uidBySummary := map[string]string{}
	statusByUID := map[string]string{}
	knownUID := map[string]bool{}
	for _, hi := range haItems {
		uidBySummary[strings.ToLower(strings.TrimSpace(hi.Summary))] = hi.UID
		statusByUID[hi.UID] = hi.Status
		knownUID[hi.UID] = true
	}

	existing, err := store.ListHASyncRows(ctx, hh.ID)
	if err != nil {
		return res, err
	}
	rowByTerm := map[string]*db.HASyncRow{}
	for _, r := range existing {
		rowByTerm[r.NormalizedTerm] = r
	}

	wantTerm := map[string]bool{}
	for _, ln := range lines {
		if ln.InPantry {
			continue
		}
		term := termFor(ctx, store, ln)
		if term == "" {
			continue
		}
		wantTerm[term] = true
		summary := label(ln, hc.ItemFormat)

		row, err := store.UpsertHASyncRow(ctx, db.UpsertHASyncRowParams{
			HouseholdID: hh.ID, ItemID: ln.ItemID, NormalizedTerm: term, HASummary: summary,
		})
		if err != nil || row == nil {
			log.Printf("ha sync: upsert map %q: %v", term, err)
			continue
		}

		uid := row.HAUID
		if uid != "" && !knownUID[uid] {
			// The uid we have on file no longer exists on HA's list - it was
			// removed there directly (or the whole list was cleared) rather
			// than through Go Eat unchecking/dropping it. Treat it the same as
			// never having been pushed, so it gets re-added below instead of
			// silently failing an update_item call against a uid HA has
			// forgotten.
			uid = ""
		}
		if uid == "" {
			uid = uidBySummary[strings.ToLower(summary)]
		}
		if uid == "" {
			if err := client.AddItem(ctx, hc.TodoEntity, summary); err != nil {
				log.Printf("ha sync: add %q: %v", summary, err)
				continue
			}
			res.Pushed++
			// Re-read to capture the uid HA assigned.
			if items, e := client.GetItems(ctx, hc.TodoEntity); e == nil {
				for _, hi := range items {
					if strings.EqualFold(strings.TrimSpace(hi.Summary), summary) {
						uid = hi.UID
						statusByUID[uid] = hi.Status
					}
				}
			}
		}

		// Mirror the local checked state onto HA. This is a straight mirror in
		// both directions: the app owns "checked" here, HA owns it in PullList,
		// and SyncOnce runs pull before push so a fresher HA-side change always
		// wins first. Gating the uncheck direction on some extra flag caused a
		// real bug: unchecking an item that was checked locally (not via HA)
		// silently failed to push back to HA.
		haStatus := statusByUID[uid]
		wantStatus := "needs_action"
		if ln.Checked {
			wantStatus = "completed"
		}
		if haStatus != wantStatus {
			if err := client.UpdateItem(ctx, hc.TodoEntity, orSummary(uid, summary), wantStatus); err == nil {
				haStatus = wantStatus
			} else {
				log.Printf("ha sync: update status %q: %v", summary, err)
			}
		}
		_ = store.SetHASyncPushed(ctx, row.ID, uid, haStatus, summary, ln.Checked)
	}

	// Lines that dropped off the plan: remove from HA and forget the mapping.
	for term, row := range rowByTerm {
		if wantTerm[term] {
			continue
		}
		ref := orSummary(row.HAUID, row.HASummary)
		if ref != "" {
			if err := client.RemoveItem(ctx, hc.TodoEntity, ref); err != nil {
				log.Printf("ha sync: remove %q: %v", row.HASummary, err)
			} else {
				res.Removed++
			}
		}
		_ = store.DeleteHASyncRow(ctx, row.ID)
	}
	return res, nil
}

// PullList reads the HA to-do list and reconciles completion back onto the
// household's current shopping list.
func PullList(ctx context.Context, store db.Store, client *Client, hc settings.HAConfig, hh *db.Household) (Result, error) {
	var res Result
	haItems, err := client.GetItems(ctx, hc.TodoEntity)
	if err != nil {
		return res, err
	}
	statusByUID := map[string]string{}
	statusBySummary := map[string]string{}
	for _, hi := range haItems {
		statusByUID[hi.UID] = hi.Status
		statusBySummary[strings.ToLower(strings.TrimSpace(hi.Summary))] = hi.Status
	}

	plan, err := store.GetLatestPlan(ctx, hh.ID)
	if err != nil || plan == nil {
		return res, err
	}
	lines, err := store.ListShoppingListItems(ctx, plan.ID)
	if err != nil {
		return res, err
	}
	lineByItemID := map[int64]*db.ShoppingListItem{}
	lineByTerm := map[string]*db.ShoppingListItem{}
	for _, ln := range lines {
		if ln.ItemID != nil {
			lineByItemID[*ln.ItemID] = ln
		}
		lineByTerm[termFor(ctx, store, ln)] = ln
	}

	rows, err := store.ListHASyncRows(ctx, hh.ID)
	if err != nil {
		return res, err
	}
	for _, row := range rows {
		haStatus, ok := statusByUID[row.HAUID]
		if !ok {
			haStatus, ok = statusBySummary[strings.ToLower(strings.TrimSpace(row.HASummary))]
		}
		if !ok {
			continue // not on the HA list right now; next push re-adds it
		}

		var ln *db.ShoppingListItem
		if row.ItemID != nil {
			ln = lineByItemID[*row.ItemID]
		}
		if ln == nil {
			ln = lineByTerm[row.NormalizedTerm]
		}
		if ln == nil {
			continue
		}

		switch {
		case haStatus == "completed" && !ln.Checked && !row.LocalChecked:
			// row.LocalChecked was false as of the last sync, so nothing here
			// unchecked this item locally in the meantime - a fresh HA-side
			// check really did happen. Without the !row.LocalChecked guard,
			// unchecking an item in Go Eat (ln.Checked flips false immediately)
			// would get overridden right back to checked on the very next sync,
			// because HA still shows the old "completed" until PushList (which
			// runs after this) tells it otherwise.
			if err := store.CheckShoppingListItem(ctx, ln.ID, true); err == nil {
				res.Checked++
				res.CheckedIDs = append(res.CheckedIDs, ln.ID)
				_ = store.SetHASyncPulled(ctx, row.ID, haStatus, true)
			}
		case haStatus != "completed" && ln.Checked && row.LocalChecked:
			// The line was completed via HA earlier and has now been unchecked
			// there - reflect that locally.
			if err := store.CheckShoppingListItem(ctx, ln.ID, false); err == nil {
				res.Unchecked++
				res.UncheckedIDs = append(res.UncheckedIDs, ln.ID)
				_ = store.SetHASyncPulled(ctx, row.ID, haStatus, false)
			}
		default:
			_ = store.SetHASyncPulled(ctx, row.ID, haStatus, ln.Checked)
		}
	}
	return res, nil
}

// orSummary picks the uid when we have one, else the summary text, as the
// reference HA services accept for an existing item.
func orSummary(uid, summary string) string {
	if strings.TrimSpace(uid) != "" {
		return uid
	}
	return summary
}
