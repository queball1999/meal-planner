package pricing

import (
	"context"
	"math"
	"strings"

	"goeat/db"
)

// PantryDeduction records what the household's existing stock covered for one
// aggregated item.
type PantryDeduction struct {
	// Used is how much of the requirement the pantry supplied, in the
	// aggregate's own unit.
	Used float64
	// Covered is true when the pantry supplied the whole requirement, so
	// there is nothing left to buy.
	Covered bool
	// PantryName is what the pantry calls it, for the "from your pantry"
	// message - the pantry row and the shopping line can be named differently.
	PantryName string
}

// ApplyPantry subtracts what the household already has from what the plan
// needs, in place.
//
// This is the missing half of a promise the app has been making since the
// pantry page was written: "Pantry items are deducted from future shopping
// lists" (spec §5.4). Nothing has ever subtracted anything, so a household
// that just stocked up was told to buy it all again and the weekly budget -
// the number the whole app is organised around - was overstated by the
// contents of the cupboard.
//
// It runs on the aggregate, before pricing, rather than filtering the finished
// list: a line removed afterwards has already been priced, counted, and in
// some paths already sent to a store lookup.
//
// Returns a deduction per aggregate index, so the caller can record on each
// shopping line what the pantry supplied.
func ApplyPantry(ctx context.Context, store db.Store, householdID int64, items []AggItem) (map[int]PantryDeduction, error) {
	if len(items) == 0 {
		return nil, nil
	}
	rows, err := store.ListPantryItems(ctx, householdID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// A household can hold the same thing under two rows (one linked to a
	// catalog item, one not), so stock is tracked per pantry row and consumed
	// from whichever row matches - never double-spent across two aggregates.
	byItem := map[int64]*stock{}
	byTerm := map[string]*stock{}
	for _, r := range rows {
		if r.QuantityOnHand <= 0 {
			continue
		}
		s := &stock{row: r, left: r.QuantityOnHand}
		if r.ItemID != nil {
			byItem[*r.ItemID] = s
		}
		term := r.NormalizedTerm
		if term == "" {
			term = Normalize(r.Name)
		}
		if term != "" {
			byTerm[term] = s
		}
	}

	out := make(map[int]PantryDeduction, len(items))
	for i := range items {
		agg := &items[i]

		// Quantities that could not be unit-reconciled are approximate by
		// construction (see AggItem.Unconverted). Subtracting a pantry
		// quantity from one is arithmetic on two numbers that are not in the
		// same unit, and the failure mode - not buying something you need -
		// is worse than the one it would fix.
		if agg.Unconverted || agg.TotalQuantity <= 0 {
			continue
		}

		s := matchStock(agg, byItem, byTerm)
		if s == nil || s.left <= 0 {
			continue
		}

		// The pantry row's unit has to agree with what the aggregate is
		// measured in. When the aggregate resolved to a catalog item its unit
		// is that item's stock unit, which is what the pantry holds too - but
		// a hand-entered pantry row can still say something else.
		haveFull, ok := pantryQuantityIn(ctx, store, s.row, agg)
		if !ok || haveFull <= 0 {
			continue
		}

		// Row units per aggregate unit. Constant, because Convert is linear -
		// which is why it can be derived from the row's full quantity and
		// still be right after the row has been partly spent. Deriving it from
		// what is *left* instead would drift on every second deduction.
		perAggUnit := s.row.QuantityOnHand / haveFull

		available := s.left / perAggUnit
		used := math.Min(available, agg.TotalQuantity)
		if used <= 0 {
			continue
		}
		remaining := round3(agg.TotalQuantity - used)

		// Spend the row in its own unit, so a second aggregate matching the
		// same row cannot spend the same stock twice.
		s.left -= used * perAggUnit
		if s.left < 0 {
			s.left = 0
		}

		agg.TotalQuantity = remaining
		out[i] = PantryDeduction{
			Used:       round3(used),
			Covered:    remaining <= 0,
			PantryName: s.row.Name,
		}
	}
	return out, nil
}

// stock is one pantry row and how much of it is still unspent, in the row's
// own unit.
type stock struct {
	row  *db.PantryItem
	left float64
}

// matchStock finds the pantry row for an aggregate.
//
// Deliberately exact: the catalog item when both sides have one, otherwise the
// normalized term. No fuzzy matching here, unlike catalog.EnsureItem - a wrong
// guess there creates a duplicate item somebody can fix, while a wrong guess
// here silently stops a household buying dinner.
func matchStock(agg *AggItem, byItem map[int64]*stock, byTerm map[string]*stock) *stock {
	if agg.ItemID != nil {
		if s, ok := byItem[*agg.ItemID]; ok {
			return s
		}
	}
	term := strings.TrimSpace(agg.NormalizedTerm)
	if term == "" {
		return nil
	}
	if s, ok := byTerm[term]; ok {
		// A pantry row linked to a *different* catalog item is not this item,
		// however similar the names are - the link is the more specific fact.
		if agg.ItemID != nil && s.row.ItemID != nil && *s.row.ItemID != *agg.ItemID {
			return nil
		}
		return s
	}
	return nil
}

// pantryQuantityIn expresses a pantry row's stock in the aggregate's unit,
// reporting false when the two units cannot be reconciled.
func pantryQuantityIn(ctx context.Context, store db.Store, row *db.PantryItem, agg *AggItem) (float64, bool) {
	from := strings.ToLower(strings.TrimSpace(row.Unit))
	to := strings.ToLower(strings.TrimSpace(agg.Unit))
	if from == to || from == "" {
		// A pantry row with no unit is assumed to be counted in whatever the
		// aggregate uses. That is a guess, but it is the same guess the pantry
		// form makes when it defaults the unit to "each".
		return row.QuantityOnHand, true
	}

	var conv []*db.UnitConversion
	if agg.ItemID != nil {
		conv, _ = store.ListConversionsForItem(ctx, *agg.ItemID)
	}
	return Convert(row.QuantityOnHand, from, to, conv)
}

// round3 matches the precision the rest of the quantity maths persists at.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
