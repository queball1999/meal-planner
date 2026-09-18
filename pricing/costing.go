package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"goeat/db"
)

// CostResult summarizes the outcome of pricing a full plan.
type CostResult struct {
	TotalCents        int64
	ConfidenceSummary string // e.g. "78% from live/cached prices, 22% estimated"
}

// loadConversions returns the conversion graph a caller needs to reconcile a
// pack's unit against an item's stock unit: the item's own edges plus every
// global edge when it is catalogued, otherwise just the global edges. Errors
// are swallowed - a missing edge only means a conversion fails to resolve,
// which the caller already handles.
func loadConversions(ctx context.Context, store db.Store, itemID *int64) []*db.UnitConversion {
	if itemID != nil {
		conv, _ := store.ListConversionsForItem(ctx, *itemID)
		return conv
	}
	conv, _ := store.ListGlobalConversions(ctx)
	return conv
}

// reconcilePack expresses one pack (packAmount of packUnit) in stockUnit and
// works out how many whole packs cover need, where need is already in
// stockUnit. reconciled is false when packUnit cannot be converted to stockUnit
// - the caller must then NOT trust packs/buyQty (they are just need passed
// through) and should treat the line as an estimate, because multiplying across
// two unrelated units is what produced "681 lb of chicken breast" and "9
// cartons of eggs".
func reconcilePack(need, packAmount float64, packUnit, stockUnit string, conv []*db.UnitConversion) (packs int, buyQty, packSize float64, reconciled bool) {
	if packAmount <= 0 {
		packAmount = 1
	}
	if need < 0 {
		need = 0
	}
	ps, ok := Convert(packAmount, packUnit, stockUnit, conv)
	if !ok || ps <= 0 {
		return 1, need, need, false
	}
	n := PacksNeeded(need, ps)
	return n, float64(n) * ps, ps, true
}

// CostPlan prices all ingredients in a plan against the given stores using the
// resolution chain, writes shopping_list_items, updates plan.total_cents and
// plan.confidence_summary, then returns a CostResult (§6.4, §7.3).
//
// stores is ordered by preference; the first store that answers for an item wins.
// If no store answers, the item is priced at zero and tagged "estimate".
func CostPlan(
	ctx context.Context,
	store db.Store,
	chain *Chain,
	planID int64,
	household *db.Household,
	stores []*db.GroceryStore,
) (*CostResult, error) {
	seeded, err := SeedShoppingList(ctx, store, planID, household)
	if err != nil {
		return nil, err
	}
	return ResolvePricing(ctx, store, chain, planID, household, stores, seeded)
}

// seededItem is one shopping-list line SeedShoppingList has already written
// as a "pending" skeleton row, carried forward so ResolvePricing knows which
// existing row to fill in rather than creating a duplicate.
type seededItem struct {
	AggItem
	RowID     int64
	Deduction PantryDeduction
}

// SeedShoppingList aggregates a plan's ingredients into shopping-list lines
// and writes them immediately as `pending` skeleton rows - no network calls,
// so the shopping list tab has something to render (skeleton loading) the
// instant a plan is marked ready, before pricing (which is what takes real
// time: a live scrape or AI price lookup per ingredient) has resolved
// anything. ResolvePricing turns each of the returned rows into a priced one.
//
// Clears any previous shopping list for this plan first (idempotent re-seed).
func SeedShoppingList(ctx context.Context, store db.Store, planID int64, household *db.Household) ([]seededItem, error) {
	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list ingredients: %w", err)
	}

	if err := store.DeleteShoppingListItems(ctx, planID); err != nil {
		return nil, fmt.Errorf("clear shopping list: %w", err)
	}

	items, err := AggregateByItem(ctx, store, household.ID, ingredients)
	if err != nil {
		return nil, fmt.Errorf("aggregate ingredients: %w", err)
	}

	// Subtract what the household already has, before anything is priced -
	// see ApplyPantry. Non-fatal: a plan that over-buys is worse than one that
	// does not, but it is still a usable plan, and losing the whole costing
	// run over a pantry read would be a bad trade.
	deducted, perr := ApplyPantry(ctx, store, household.ID, items)
	if perr != nil {
		log.Printf("costing: pantry deduction: %v", perr)
	}

	return seedItems(ctx, store, planID, items, deducted)
}

// seedItems is SeedShoppingList's per-item write loop, factored out so
// RescaleShoppingList can seed just the lines that actually need a fresh
// price (a new or previously-unpriced ingredient) without touching the lines
// it has already rescaled in place. items and deductions must be the same
// length and index-aligned (as AggregateByItem/ApplyPantry produce them).
func seedItems(ctx context.Context, store db.Store, planID int64, items []AggItem, deductions map[int]PantryDeduction) ([]seededItem, error) {
	seeded := make([]seededItem, 0, len(items))
	for idx, item := range items {
		ded := deductions[idx]
		refsJSON, _ := json.Marshal(item.IngredientIDs)
		row, cerr := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID:             planID,
			ItemID:             item.ItemID,
			MealIngredientRefs: string(refsJSON),
			DisplayName:        item.DisplayName,
			BuyQuantity:        item.TotalQuantity,
			PackSize:           item.TotalQuantity,
			PurchaseUnit:       item.Unit,
			PriceSource:        ConfidenceEstimate,
			Confidence:         ConfidenceEstimate,
			PantryQtyUsed:      ded.Used,
			InPantry:           ded.Covered,
			Pending:            true,
		})
		if cerr != nil {
			log.Printf("costing: seed shopping list item %q: %v", item.DisplayName, cerr)
			continue
		}
		seeded = append(seeded, seededItem{AggItem: item, RowID: row.ID, Deduction: ded})
	}
	return seeded, nil
}

// maxConcurrentStoreLookups bounds how many stores' resolveFirstStore fans
// out to at once. Uncapped concurrency here would mean one ingredient with a
// dozen configured stores opens a dozen simultaneous scrape/render calls;
// this keeps it to a sane number regardless of how many stores a household has.
const maxConcurrentStoreLookups = 6

// storeHit is one store's answer, for resolveFirstStore's result channel.
type storeHit struct {
	result  *PriceResult
	storeID int64
}

// resolveFirstStore tries chain.resolveExcluding against every store
// concurrently (bounded by maxConcurrentStoreLookups) and returns the first
// one that answers, canceling the rest. A scrape lookup can legitimately take
// up to 90-180s before falling through (pricing/scraper.go's
// scrapeTimeout/scrapeAITimeout), so trying stores one at a time - as this
// used to - could mean minutes per ingredient once a household has several
// configured. Tie-breaking is by completion order rather than by stores'
// list order: the point of running them concurrently is to not wait out a
// slow store just because an earlier one in the list would also have
// answered.
func resolveFirstStore(ctx context.Context, chain *Chain, term string, stores []*db.GroceryStore, region string) (*PriceResult, int64, bool) {
	if len(stores) == 0 {
		return nil, 0, false
	}

	resolveCtx, cancel := context.WithCancel(ctx)
	defer cancel() // stop every other in-flight lookup once we have a winner (or none does)

	hits := make(chan storeHit, len(stores)) // buffered: a losing goroutine's send never blocks
	sem := make(chan struct{}, min(len(stores), maxConcurrentStoreLookups))
	var wg sync.WaitGroup

	for _, gs := range stores {
		wg.Add(1)
		sem <- struct{}{}
		go func(gs *db.GroceryStore) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := chain.resolveExcluding(resolveCtx, term, gs.ID, region, aiEstimateProviderName)
			if err != nil {
				if resolveCtx.Err() == nil { // don't log the ones we canceled ourselves
					log.Printf("costing: %v", err)
				}
				return
			}
			if r != nil {
				hits <- storeHit{result: r, storeID: gs.ID}
			}
		}(gs)
	}
	go func() {
		wg.Wait()
		close(hits)
	}()

	winner, ok := <-hits
	if !ok {
		return nil, 0, false
	}
	return winner.result, winner.storeID, true
}

// lineState is one shopping-list line's pricing progress, carried between
// ResolvePricing's resolve pass and its finalize pass so the AI-estimate step
// in between can fill in whatever the resolve pass couldn't.
type lineState struct {
	priceCents      int64
	priceSource     string
	confidence      string
	resolvedStoreID *int64
	packAmount      float64
	packUnit        string
	priced          bool
}

// ResolvePricing prices each of a plan's already-seeded shopping-list lines:
// a resolve pass (store package, then the chain minus AI estimate) for every
// line, then one batched AI-estimate call for whatever the resolve pass
// couldn't price (see BatchAIEstimates/AIEstimateProvider.LookupBatch) rather
// than an AI lookup per ingredient, then a finalize pass that reconciles
// units and writes each row.
//
// Cancel ctx (e.g. a "stop pricing" request) to abort early: any resolve or
// batch call already in flight fails fast, and every line - reached or not -
// still gets finalized and written, falling through to its own tier-3/default
// fallback exactly as if nothing further had ever answered it.
func ResolvePricing(
	ctx context.Context,
	store db.Store,
	chain *Chain,
	planID int64,
	household *db.Household,
	stores []*db.GroceryStore,
	seeded []seededItem,
) (*CostResult, error) {
	region := household.ZIPCode

	lines := make([]lineState, len(seeded))
	var missIdx []int

	for i, seed := range seeded {
		item := seed.AggItem
		ls := lineState{packAmount: 1}

		// 1. Prefer a per-store package for a catalogued item: it carries the
		//    real "amount per package" in a known unit, so pack maths is exact.
		if item.ItemID != nil {
			for _, gs := range stores {
				pkg, perr := store.GetItemStorePackage(ctx, *item.ItemID, gs.ID)
				if perr != nil {
					log.Printf("costing: package lookup: %v", perr)
					continue
				}
				if pkg == nil {
					continue
				}
				ls.packAmount = pkg.AmountPerPackage
				ls.packUnit = pkg.PurchaseUnit
				ls.priceCents = pkg.PriceCents
				ls.priceSource = "manual"
				ls.confidence = ConfidenceManual
				sid := gs.ID
				ls.resolvedStoreID = &sid
				ls.priced = true
				break
			}
		}

		// 2. Fall back to the resolution chain, minus the AI estimate provider -
		//    a chain miss here is batched into one AI-estimate call below
		//    instead of asking the LLM once per ingredient. Stores are tried
		//    concurrently (resolveFirstStore) rather than one at a time: a
		//    scrape lookup can legitimately take up to 90-180s
		//    (pricing/scraper.go's scrapeTimeout/scrapeAITimeout) before
		//    falling through, so a sequential loop over several stores could
		//    mean minutes per ingredient in the worst case.
		if !ls.priced {
			if r, sid, ok := resolveFirstStore(ctx, chain, item.NormalizedTerm, stores, region); ok {
				ls.packAmount = r.PackSize
				ls.packUnit = r.PurchaseUnit
				ls.priceCents = r.PriceCents
				ls.priceSource = r.Source
				ls.confidence = r.Confidence
				ls.resolvedStoreID = &sid
				ls.priced = true
			}
		}

		if !ls.priced && len(stores) > 0 {
			missIdx = append(missIdx, i)
		}
		lines[i] = ls
	}

	// 2b. Batch every chain miss into as few AI-estimate calls as possible.
	// stores[0].ID is the same store a per-item AI lookup would have landed
	// on: the old per-store loop always resolved (and cached) an AI-only
	// answer against the first store it tried, since the provider ignores
	// storeID entirely.
	if ai := chain.aiEstimateProvider(); ai != nil && len(missIdx) > 0 {
		terms := make([]string, len(missIdx))
		for j, i := range missIdx {
			terms[j] = seeded[i].AggItem.NormalizedTerm
		}
		estimates := ai.LookupBatch(ctx, terms, region)
		sid := stores[0].ID
		for j, i := range missIdx {
			r := estimates[j]
			if r == nil {
				continue
			}
			ls := &lines[i]
			ls.packAmount = r.PackSize
			ls.packUnit = r.PurchaseUnit
			ls.priceCents = r.PriceCents
			ls.priceSource = r.Source
			ls.confidence = r.Confidence
			ls.resolvedStoreID = &sid
			ls.priced = true
			if chain.persistFn != nil {
				if werr := chain.persistFn(ctx, r, sid, Normalize(terms[j])); werr != nil {
					log.Printf("pricing: persist write-back error: %v", werr)
				}
			}
		}
	}

	// Finalize pass: reconcile units and write every row. Detached from ctx -
	// a canceled ctx (stop pricing) must still let whatever was already
	// resolved above land in the database instead of stranding it mid-write.
	writeCtx := context.WithoutCancel(ctx)

	var totalCents int64
	confidenceCounts := map[string]int{}

	for i, seed := range seeded {
		item := seed.AggItem
		ls := lines[i]
		conv := loadConversions(writeCtx, store, item.ItemID)

		// 3. Last resort: the meal-planning LLM's own price guess, captured per
		// ingredient at generation time (plan.systemPrompt requires it). Used
		// only when nothing above - including a batched AI estimate - could
		// price the item, e.g. no stores configured, every provider errored,
		// the LLM provider was since removed, or pricing was stopped early.
		// Its guess is for exactly TotalQuantity of the recipe's own unit, so
		// the pack is that whole amount and reconciliation is a no-op.
		if !ls.priced && item.EstPriceCents > 0 {
			ls.packAmount = item.TotalQuantity
			ls.packUnit = item.Unit
			ls.priceCents = item.EstPriceCents
			ls.priceSource = "estimate"
			ls.confidence = ConfidenceEstimate
			ls.priced = true
		}

		var buyQuantity, packSize float64
		var lineTotal int64
		var rawPackAmount float64
		var rawPackUnit string
		purchaseUnit := item.Unit
		if ls.priced {
			packs, bq, ps, reconciled := reconcilePack(item.TotalQuantity, ls.packAmount, ls.packUnit, item.Unit, conv)
			buyQuantity, packSize = bq, ps
			if reconciled {
				lineTotal = ls.priceCents * int64(packs)
				purchaseUnit = ls.packUnit
				// Kept so a later guest/skip-meal change can redo just this
				// pack math (pricing.RescaleShoppingList) instead of asking a
				// provider again - see pack_amount/pack_unit (00030).
				rawPackAmount = ls.packAmount
				rawPackUnit = ls.packUnit
			} else {
				// packUnit and the stock unit don't connect on the conversion
				// graph, so how many packs cover the need is unknowable - and
				// guessing it is exactly the "9 cartons of eggs" bug. Buy the
				// recipe amount as-is, price it from the LLM's own per-quantity
				// guess where there is one, and let the estimate badge show.
				ls.priceSource = "estimate"
				ls.confidence = ConfidenceEstimate
				if item.EstPriceCents > 0 {
					lineTotal = item.EstPriceCents
				} else {
					lineTotal = ls.priceCents
				}
			}
		} else {
			// Truly nothing to go on: no live price, no chain estimate, and no
			// initial LLM guess either. Show the recipe's own quantity/unit
			// instead of rounding up to whole units in a blank purchase unit.
			ls.priceSource = "estimate"
			ls.confidence = ConfidenceEstimate
			packSize = item.TotalQuantity
			buyQuantity = item.TotalQuantity
		}
		// A line the pantry covered entirely stays on the list but is not
		// bought, so it must not reach the total either - the same rule the
		// "I already have this" control follows.
		ded := seed.Deduction
		if !ded.Covered {
			totalCents += lineTotal
		}
		confidenceCounts[ls.confidence]++

		// Turn this line's "pending" skeleton row (SeedShoppingList) into a
		// priced one in place, rather than inserting a duplicate - every other
		// still-pending row stays a skeleton on the shopping list tab until its
		// own turn comes up.
		if uerr := store.UpdateShoppingListItemPrice(writeCtx, db.UpdateShoppingListItemPriceParams{
			ID:             seed.RowID,
			StoreID:        ls.resolvedStoreID,
			ItemID:         item.ItemID,
			BuyQuantity:    buyQuantity,
			PackSize:       packSize,
			PurchaseUnit:   purchaseUnit,
			UnitPriceCents: ls.priceCents,
			LineTotalCents: lineTotal,
			PriceSource:    ls.priceSource,
			Confidence:     ls.confidence,
			PantryQtyUsed:  ded.Used,
			InPantry:       ded.Covered,
			PackAmount:     rawPackAmount,
			PackUnit:       rawPackUnit,
		}); uerr != nil {
			log.Printf("costing: price shopping list item %q: %v", item.DisplayName, uerr)
		}
	}

	summary := BuildConfidenceSummary(confidenceCounts, len(seeded))

	if err := store.UpdatePlanTotal(writeCtx, planID, totalCents, summary); err != nil {
		return nil, fmt.Errorf("update plan total: %w", err)
	}

	return &CostResult{
		TotalCents:        totalCents,
		ConfidenceSummary: summary,
	}, nil
}

// BuildConfidenceSummary renders the plan-level "78% from live/cached prices,
// 22% estimated" line from a tally of shopping-list line confidences.
func BuildConfidenceSummary(counts map[string]int, total int) string {
	if total == 0 {
		return "no items"
	}
	live := counts[ConfidenceLive] + counts[ConfidenceCached] + counts[ConfidenceManual] + counts[ConfidenceScrape]
	est := counts[ConfidenceEstimate]
	livePct := live * 100 / total
	estPct := est * 100 / total
	if estPct == 0 {
		return fmt.Sprintf("100%% from live/cached prices")
	}
	if livePct == 0 {
		return fmt.Sprintf("100%% estimated")
	}
	return fmt.Sprintf("%d%% from live/cached prices, %d%% estimated", livePct, estPct)
}

// EnsureShoppingList guarantees a plan has a shopping list, whatever happened
// during pricing. CostPlan is the normal path and writes a fully priced list,
// but it is optional (no pricing chain configured) and its failures are
// non-fatal, which used to leave a finished plan with an empty list and no way
// to shop it. This runs after pricing: if the list already has lines it is a
// no-op, otherwise it writes one unpriced line per aggregated ingredient using
// the plan LLM's own price guess where there is one.
//
// Returns the number of lines it created (0 when the list was already there).
func EnsureShoppingList(ctx context.Context, store db.Store, planID int64, household *db.Household) (int, error) {
	existing, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("list shopping items: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}

	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("list ingredients: %w", err)
	}
	if len(ingredients) == 0 {
		return 0, nil
	}

	items, err := AggregateByItem(ctx, store, household.ID, ingredients)
	if err != nil {
		return 0, fmt.Errorf("aggregate ingredients: %w", err)
	}

	var written int
	var totalCents int64
	for _, item := range items {
		refsJSON, _ := json.Marshal(item.IngredientIDs)
		// No pack maths is possible without a resolved package, so the line
		// buys exactly the recipe quantity and carries the LLM's guess (if any)
		// as the line total.
		lineTotal := item.EstPriceCents
		totalCents += lineTotal
		if _, cerr := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID:             planID,
			ItemID:             item.ItemID,
			MealIngredientRefs: string(refsJSON),
			DisplayName:        item.DisplayName,
			BuyQuantity:        item.TotalQuantity,
			PackSize:           item.TotalQuantity,
			PurchaseUnit:       item.Unit,
			UnitPriceCents:     lineTotal,
			LineTotalCents:     lineTotal,
			PriceSource:        "estimate",
			Confidence:         ConfidenceEstimate,
		}); cerr != nil {
			log.Printf("costing: fallback shopping line %q: %v", item.DisplayName, cerr)
			continue
		}
		written++
	}

	if written > 0 {
		summary := BuildConfidenceSummary(map[string]int{ConfidenceEstimate: written}, written)
		if err := store.UpdatePlanTotal(ctx, planID, totalCents, summary); err != nil {
			log.Printf("costing: fallback plan total: %v", err)
		}
	}
	return written, nil
}
