package pricing

import (
	"context"
	"log"
	"strings"

	"goeat/db"
)

// deductHave works out, per aggregate index, what the household already has
// and so does not need to buy: pantry stock (ApplyPantry), the food it named
// as on hand when it generated the plan (ApplyOnHand), and any line it had
// already ticked "I already have this" on the list being rebuilt
// (carryHaveTicks). Every shopping-list build - SeedShoppingList,
// RescaleShoppingList, EnsureShoppingList - goes through here, so a line the
// household said it has cannot come back as something to buy just because the
// list was rebuilt. Each source is non-fatal: over-buying beats no list.
func deductHave(ctx context.Context, store db.Store, householdID, planID int64, items []AggItem, prior []*db.ShoppingListItem) map[int]PantryDeduction {
	deducted, err := ApplyPantry(ctx, store, householdID, items)
	if err != nil {
		log.Printf("costing: pantry deduction: %v", err)
	}
	if deducted == nil {
		deducted = map[int]PantryDeduction{}
	}

	onHand, err := store.GetPlanOnHand(ctx, planID)
	if err != nil {
		log.Printf("costing: on-hand list for plan %d: %v", planID, err)
	}
	ApplyOnHand(ctx, store, householdID, onHand, items, deducted)
	carryHaveTicks(prior, items, deducted)
	return deducted
}

// ApplyOnHand marks every aggregate matching a name on the plan's on-hand list
// as covered - the same state the list's "I already have this" control sets.
// The quantity is left alone, as that control leaves it: the household said
// it has the food, not how much, so there is no pantry arithmetic to show.
func ApplyOnHand(ctx context.Context, store db.Store, householdID int64, onHand []string, items []AggItem, deducted map[int]PantryDeduction) {
	if len(onHand) == 0 || len(items) == 0 {
		return
	}

	type have struct {
		term   string
		itemID *int64
	}
	haves := make([]have, 0, len(onHand))
	for _, name := range onHand {
		term := Normalize(name)
		if term == "" {
			continue
		}
		h := have{term: term}
		// The catalog link is the precise match: "scallions" typed on the form
		// and "green onions" in a recipe are one item once an alias joins them.
		if it, _ := store.GetItemByTerm(ctx, householdID, term); it != nil {
			h.itemID = &it.ID
		} else if it, _ := store.GetItemByAlias(ctx, householdID, term); it != nil {
			h.itemID = &it.ID
		}
		haves = append(haves, h)
	}

	for i := range items {
		if deducted[i].Covered {
			continue
		}
		agg := &items[i]
		for _, h := range haves {
			if onHandMatches(agg, h.term, h.itemID) {
				d := deducted[i]
				d.Covered = true
				deducted[i] = d
				break
			}
		}
	}
}

// onHandMatches decides whether an on-hand name covers an aggregate: same
// catalog item, same normalized term, or the name's words all appear in the
// ingredient with the same head noun - "chicken thighs" covers "boneless
// chicken thighs" and "rice" covers "brown rice", but "garlic" does not cover
// "garlic powder" and "chicken" does not cover "chicken broth". Stricter than
// a substring match on purpose: a false match means not buying dinner.
func onHandMatches(agg *AggItem, term string, itemID *int64) bool {
	if itemID != nil && agg.ItemID != nil && *itemID == *agg.ItemID {
		return true
	}
	for _, cand := range []string{agg.NormalizedTerm, Normalize(agg.DisplayName)} {
		if cand == "" {
			continue
		}
		if cand == term || headNounMatch(term, cand) {
			return true
		}
	}
	return false
}

func headNounMatch(have, need string) bool {
	h := singularTokens(have)
	n := singularTokens(need)
	if len(h) == 0 || len(n) == 0 || h[len(h)-1] != n[len(n)-1] {
		return false
	}
	inNeed := make(map[string]bool, len(n))
	for _, t := range n {
		inNeed[t] = true
	}
	for _, t := range h {
		if !inNeed[t] {
			return false
		}
	}
	return true
}

// singularTokens splits a normalized term into words with a trailing plural
// "s" dropped, so "thighs" and "thigh" compare equal word by word (Normalize
// only singularizes the phrase's last letter).
func singularTokens(term string) []string {
	f := strings.Fields(term)
	for i, t := range f {
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			f[i] = strings.TrimSuffix(t, "s")
		}
	}
	return f
}

// carryHaveTicks keeps a line the household ticked "I already have this" on
// the list being rebuilt ticked on the new one. Matched the same way the
// rescale path matches rows (itemKey), so it only ever carries a tick to the
// same ingredient.
func carryHaveTicks(prior []*db.ShoppingListItem, items []AggItem, deducted map[int]PantryDeduction) {
	if len(prior) == 0 {
		return
	}
	ticked := map[string]bool{}
	for _, row := range prior {
		if row.InPantry {
			ticked[existingKey(row)] = true
		}
	}
	if len(ticked) == 0 {
		return
	}
	for i, it := range items {
		if ticked[itemKey(it.ItemID, it.NormalizedTerm)] {
			d := deducted[i]
			d.Covered = true
			deducted[i] = d
		}
	}
}
