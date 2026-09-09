package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

// shoppingStoreGroup groups shopping list items under one store heading.
type shoppingStoreGroup struct {
	StoreName     string
	SubtotalLabel string
	Items         []shoppingLineItem
}

// shoppingLineItem is one row in the shopping list display.
type shoppingLineItem struct {
	ID          int64
	DisplayName string
	BuyLabel    string // "2 lb" | "3 each"
	PriceLabel  string // "$2.49 / lb"
	TotalLabel  string // "$4.98"
	BadgeClass  string // "badge-live" | "badge-cached" | "badge-manual" | "badge-estimate"
	BadgeText   string // "Live" | "Cached" | "Manual" | "Estimated"
	Checked     bool
	Meals       []mealTag // which meal(s) this line was pulled from, deduped, color-coded

	// InPantry is the "I already have this" state: the line is kept on the
	// list for reference but is not something to buy, so it is excluded from
	// the estimated total and skipped by the Home Assistant sync. Distinct
	// from Checked, which means "picked up on this trip".
	InPantry bool
	// The raw numbers behind BuyLabel, so the have-it dialog can prefill the
	// quantity it will stock the pantry with.
	BuyQuantity float64
	Unit        string

	// LinkState is "linked" (green) or "unmatched" (yellow); LinkLabel is the
	// chip's tooltip. See lineLinkState.
	LinkState string
	LinkLabel string

	// PantryNote explains a quantity the pantry reduced ("1 lb already in your
	// pantry"). Empty when the pantry contributed nothing. Without it a line
	// that quietly shrank reads as a bug in the plan.
	PantryNote string

	// PriceVerdict is "good price" / "above usual" when this line's price is
	// notably off that item's own past prices at the same store, and empty
	// otherwise. Most prices are ordinary, and a badge on every line says
	// nothing - silence is the common case by design.
	PriceVerdict      string
	PriceVerdictClass string
}

// mealTag is one pill on a shopping-list line naming a meal it belongs to.
// ColorClass is a stable hash of the title, so the same meal always gets the
// same color everywhere it appears on the list.
type mealTag struct {
	Title      string
	ColorClass string
}

// mealColorClass picks a pill color from the meal title deterministically, so
// repeated views (and different lines that share a meal) get a consistent
// color. It is the shared 8-hue palette every other color-scanned column uses
// - see web/pills.go.
func mealColorClass(title string) string {
	return hueClass(title)
}

type shoppingListPageData struct {
	HasPlan         bool
	PlanID          int64
	WeekStart       string
	TotalLabel      string
	BudgetLabel     string
	OverBudget      bool
	Groups          []shoppingStoreGroup
	UnassignedItems []shoppingLineItem
	HALastSync      string // "5m ago" etc, from the ha_sync_map timestamps; "" if never
	ReadOnly        bool   // viewing a past/other week via ?week= or ?plan_id= - no editing
}

// haLastSyncLabel returns a short "5m ago"-style phrase for the most recent
// push or pull to Home Assistant for this household, or "" if it has never
// synced. Read from ha_sync_map, which is the only record of when a sync ran.
func (s *Server) haLastSyncLabel(ctx context.Context, householdID int64) string {
	rows, err := s.store.ListHASyncRows(ctx, householdID)
	if err != nil || len(rows) == 0 {
		return ""
	}
	var latest time.Time
	for _, row := range rows {
		for _, ts := range []string{row.LastPushedAt, row.LastPulledAt} {
			if ts == "" {
				continue
			}
			if t, perr := time.Parse(time.RFC3339Nano, ts); perr == nil && t.After(latest) {
				latest = t
			}
		}
	}
	if latest.IsZero() {
		return ""
	}
	return humaniseSince(time.Since(latest))
}

// handleShoppingListRedirect keeps the old /list URL working - the shopping
// list now lives as a tab on /plan.
func (s *Server) handleShoppingListRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/plan/list", http.StatusMovedPermanently)
}

// buildShoppingListView assembles the shopping-list view model for the /plan
// "Shopping list" tab. p is whichever plan handlePlanPage already resolved
// (the latest one, or a specific past week from ?week=/?plan_id=) so the two
// tabs always agree on which week they're showing; readOnly disables every
// control that would mutate it - a past week's list is a record, not a cart.
func (s *Server) buildShoppingListView(ctx context.Context, hh *db.Household, p *db.Plan, readOnly bool) shoppingListPageData {
	if p == nil || p.Status == "generating" {
		return shoppingListPageData{HasPlan: false}
	}

	rawItems, _ := s.store.ListShoppingListItems(ctx, p.ID)
	stores, _ := s.store.ListStores(ctx, hh.ID)
	mealTitles, _ := s.store.ListMealTitlesByIngredientID(ctx, p.ID)

	storeMap := make(map[int64]string, len(stores))
	for _, gs := range stores {
		storeMap[gs.ID] = gs.Name
	}

	// The catalog, keyed by id, so each line can say whether it resolves to a
	// real item or only to an auto-created placeholder. One query for the whole
	// list rather than a lookup per line.
	catalogItems, _ := s.store.ListItems(ctx, hh.ID)
	itemsByID := make(map[int64]*db.Item, len(catalogItems))
	for _, it := range catalogItems {
		itemsByID[it.ID] = it
	}
	verdicts := s.priceVerdicts(ctx, rawItems)

	// Group items by store.
	groupIndex := make(map[int64]int)
	var groups []shoppingStoreGroup
	var unassigned []shoppingLineItem

	for _, item := range rawItems {
		line := buildLineItem(item, mealTitles, itemsByID, verdicts)
		if item.StoreID == nil {
			unassigned = append(unassigned, line)
			continue
		}
		sid := *item.StoreID
		if i, ok := groupIndex[sid]; ok {
			groups[i].Items = append(groups[i].Items, line)
		} else {
			groupIndex[sid] = len(groups)
			groups = append(groups, shoppingStoreGroup{
				StoreName: storeMap[sid],
				Items:     []shoppingLineItem{line},
			})
		}
	}

	total, storeSubs := summarizeLines(rawItems)
	for i, g := range groups {
		var sid int64
		for k, v := range groupIndex {
			if v == i {
				sid = k
				break
			}
		}
		groups[i].SubtotalLabel = fmt.Sprintf("$%.2f", float64(storeSubs[sid])/100)
		_ = g
	}

	return shoppingListPageData{
		HasPlan:         true,
		PlanID:          p.ID,
		WeekStart:       p.WeekStart,
		TotalLabel:      fmt.Sprintf("$%.2f", float64(total)/100),
		BudgetLabel:     fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100),
		OverBudget:      total > p.BudgetCents && total > 0,
		Groups:          groups,
		UnassignedItems: unassigned,
		HALastSync:      s.haLastSyncLabel(ctx, hh.ID),
		ReadOnly:        readOnly,
	}
}

// summarizeLines adds up what the trip will cost, in total and per store.
//
// The total used to be read from plan.total_cents, which only UpdatePlanTotal
// writes - so every other path that changes a price (the pencil editor, a
// headcount rescale, the unpriced-list fallback) left it stale, and a list
// full of priced lines reported "$0.00 estimated total". Summing the current
// lines cannot go stale by construction.
//
// A line marked "I already have this" is excluded: it stays on the list for
// reference but is not being bought, so counting it would overstate the trip.
func summarizeLines(items []*db.ShoppingListItem) (total int64, byStore map[int64]int64) {
	byStore = make(map[int64]int64)
	for _, item := range items {
		if item.InPantry {
			continue
		}
		total += item.LineTotalCents
		if item.StoreID != nil {
			byStore[*item.StoreID] += item.LineTotalCents
		}
	}
	return total, byStore
}

func buildLineItem(item *db.ShoppingListItem, mealTitleByIngredient map[int64]string, itemsByID map[int64]*db.Item, verdicts map[int64]priceFlag) shoppingLineItem {
	var linked *db.Item
	if item.ItemID != nil {
		linked = itemsByID[*item.ItemID]
	}

	// BuyQuantity is stored in the item's stock unit (costing.go reconciles
	// every pack into it), so the label has to read in that unit too - not
	// PurchaseUnit, which is only the store's word for a pack ("bag", "lb")
	// and was what made a 907 g buy render as "907 lb".
	qtyUnit := item.PurchaseUnit
	if linked != nil && linked.StockUnit != "" {
		qtyUnit = linked.StockUnit
	}
	buyLabel := qtyLabel(item.BuyQuantity, qtyUnit)
	priceLabel := ""
	if item.UnitPriceCents > 0 {
		priceLabel = fmt.Sprintf("$%.2f / %s", float64(item.UnitPriceCents)/100, item.PurchaseUnit)
	}
	totalLabel := fmt.Sprintf("$%.2f", float64(item.LineTotalCents)/100)

	badgeClass, badgeText := confidenceBadge(item.Confidence)

	state := lineLinkState(linked)
	label := "Not matched to a known item yet - click to pick one"
	if state == "linked" {
		label = "Linked to " + linked.Name
	}

	return shoppingLineItem{
		ID:                item.ID,
		DisplayName:       item.DisplayName,
		BuyLabel:          buyLabel,
		PriceLabel:        priceLabel,
		TotalLabel:        totalLabel,
		BadgeClass:        badgeClass,
		BadgeText:         badgeText,
		Checked:           item.Checked,
		Meals:             mealTagsFor(item.MealIngredientRefs, mealTitleByIngredient),
		InPantry:          item.InPantry,
		BuyQuantity:       item.BuyQuantity,
		Unit:              item.PurchaseUnit,
		LinkState:         state,
		LinkLabel:         label,
		PantryNote:        pantryNote(item),
		PriceVerdict:      verdicts[item.ID].Label,
		PriceVerdictClass: verdicts[item.ID].Class,
	}
}

// priceFlag is one line's verdict against its own price history.
type priceFlag struct {
	Label string
	Class string
}

// priceVerdicts judges each priced line against that item's past prices at the
// same store.
//
// Same store only. Two shops' prices for the same thing are not comparable, so
// "cheap" measured against a blend of them would be noise dressed as a signal
// - a line at a premium grocer would read as expensive purely for being there.
//
// Lines with no item, no store, or no price are skipped: there is nothing to
// compare them against, and guessing is worse than saying nothing.
func (s *Server) priceVerdicts(ctx context.Context, lines []*db.ShoppingListItem) map[int64]priceFlag {
	out := map[int64]priceFlag{}
	// One history read per (item, store), not per line - a list routinely has
	// several lines from the same shop.
	type key struct{ item, store int64 }
	cache := map[key][]float64{}

	for _, ln := range lines {
		if ln.ItemID == nil || ln.StoreID == nil || ln.UnitPriceCents <= 0 {
			continue
		}
		k := key{*ln.ItemID, *ln.StoreID}
		prior, ok := cache[k]
		if !ok {
			entries, err := s.store.ListPriceHistory(ctx, k.item, k.store)
			if err != nil {
				cache[k] = nil
				continue
			}
			for _, e := range entries {
				prior = append(prior, unitPrice(e.PriceCents, e.AmountPerPackage))
			}
			cache[k] = prior
		}

		current := unitPrice(ln.UnitPriceCents, ln.PackSize)
		if label, class := priceVerdict(current, prior); label != "" {
			out[ln.ID] = priceFlag{Label: label, Class: class}
		}
	}
	return out
}

// pantryNote renders what the household's own stock covered on this line.
//
// Two different sentences on purpose: a line the pantry covered entirely is
// not being bought at all, and saying "2 lb already in your pantry" next to a
// quantity of zero invites the reader to work out the subtraction themselves.
func pantryNote(item *db.ShoppingListItem) string {
	if item.PantryQtyUsed <= 0 {
		return ""
	}
	unit := item.PurchaseUnit
	if unit == "" {
		unit = "on hand"
	}
	if item.InPantry {
		return fmt.Sprintf("all %.4g %s already in your pantry", item.PantryQtyUsed, unit)
	}
	return fmt.Sprintf("%.4g %s already in your pantry", item.PantryQtyUsed, unit)
}

// mealTagsFor resolves a shopping-list line's meal_ingredient_refs (a JSON
// []int64) to the distinct meal titles they came from, in first-seen order.
func mealTagsFor(refsJSON string, mealTitleByIngredient map[int64]string) []mealTag {
	if refsJSON == "" || len(mealTitleByIngredient) == 0 {
		return nil
	}
	var refs []int64
	if err := json.Unmarshal([]byte(refsJSON), &refs); err != nil {
		return nil
	}
	seen := make(map[string]bool, len(refs))
	var tags []mealTag
	for _, id := range refs {
		title := mealTitleByIngredient[id]
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		tags = append(tags, mealTag{Title: title, ColorClass: mealColorClass(title)})
	}
	return tags
}

func confidenceBadge(conf string) (class, text string) {
	switch conf {
	case "live":
		return "badge-live", "Live"
	case "cached":
		return "badge-cached", "Cached"
	case "manual":
		return "badge-manual", "Manual"
	case "scrape":
		return "badge-cached", "Scraped"
	default:
		return "badge-estimate", "Estimated"
	}
}

// handleShoppingListExport streams the current shopping list as a downloadable
// CSV (the only format for now; ?format is accepted for forward compat).
func (s *Server) handleShoppingListExport(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		http.Error(w, "no plan", http.StatusNotFound)
		return
	}
	items, _ := s.store.ListShoppingListItems(ctx, p.ID)
	stores, _ := s.store.ListStores(ctx, hh.ID)
	storeName := map[int64]string{}
	for _, gs := range stores {
		storeName[gs.ID] = gs.Name
	}
	itemsByID := map[int64]*db.Item{}
	if catalogItems, _ := s.store.ListItems(ctx, hh.ID); catalogItems != nil {
		for _, it := range catalogItems {
			itemsByID[it.ID] = it
		}
	}
	prefs, _ := s.store.GetPreferences(ctx, hh.ID)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shopping-list-%s.csv"`, p.WeekStart))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Store", "Item", "Buy quantity", "Unit", "Line total", "Checked"})
	for _, it := range items {
		store := "Unassigned"
		if it.StoreID != nil {
			if n := storeName[*it.StoreID]; n != "" {
				store = n
			}
		}
		checked := ""
		if it.Checked {
			checked = "yes"
		}
		// Same stock-unit / preferred-system treatment the on-screen list gets.
		qtyUnit := it.PurchaseUnit
		if it.ItemID != nil {
			if linked := itemsByID[*it.ItemID]; linked != nil && linked.StockUnit != "" {
				qtyUnit = linked.StockUnit
			}
		}
		qty, unit := displayQtyUnit(it.BuyQuantity, qtyUnit, prefs.UnitSystem)
		_ = cw.Write([]string{
			store,
			it.DisplayName,
			fmt.Sprintf("%.4g", qty),
			unit,
			fmt.Sprintf("%.2f", float64(it.LineTotalCents)/100),
			checked,
		})
	}
	cw.Flush()
}

func (s *Server) handleShoppingListCheck(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	checked := r.FormValue("checked") == "1"
	if err := s.store.CheckShoppingListItem(r.Context(), id, checked); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleShoppingListHave toggles a line's "I already have this" state.
//
// This is a different thing from the check-off box next to it. Checked means
// "picked this up on the trip"; already-have means "it is in the cupboard, do
// not buy it" - so an already-have line drops out of the estimated total and
// is skipped by the Home Assistant sync, while a checked one still counts.
//
// Turning it on also stocks the pantry, because saying you have something and
// then not having it recorded anywhere is how the pantry drifts out of date.
// The quantity comes from the request, defaulting to the amount the line was
// going to buy.
//
// It only creates a pantry row that is *missing*. CreatePantryItem's upsert
// adds to an existing quantity, so re-ticking a line the user already tracks
// would silently inflate their stock every time.
func (s *Server) handleShoppingListHave(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	have := r.FormValue("have") == "1"
	ctx := r.Context()

	if err := s.store.MarkShoppingListItemInPantry(ctx, id, have); err != nil {
		log.Printf("list have: mark %d: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't save that"})
		return
	}
	if !have {
		// Un-ticking does not take anything back out of the pantry: you did
		// have it, and how much is left is the pantry page's business.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "have": false})
		return
	}

	line := s.shoppingLineByID(ctx, hh.ID, id)
	if line == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "have": true})
		return
	}

	qty := line.BuyQuantity
	if raw := strings.TrimSpace(r.FormValue("quantity")); raw != "" {
		if q, perr := strconv.ParseFloat(raw, 64); perr == nil && q > 0 {
			qty = q
		}
	}
	unit := strings.TrimSpace(r.FormValue("unit"))
	if unit == "" {
		unit = line.PurchaseUnit
	}

	term := pricing.Normalize(line.DisplayName)
	existing, _ := s.store.GetPantryItemByTerm(ctx, hh.ID, term)
	if existing != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "have": true, "pantry": "existing",
			"message": fmt.Sprintf("%s is already in your pantry - quantity left as it was.", existing.Name),
		})
		return
	}

	pi, cerr := s.store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           line.DisplayName,
		NormalizedTerm: term,
		QuantityOnHand: qty,
		Unit:           unit,
	})
	if cerr != nil {
		log.Printf("list have: stock pantry for %d: %v", id, cerr)
		// The line is marked either way; only the pantry write failed.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "have": true, "pantry": "failed",
			"message": "Marked as already have, but couldn't add it to the pantry.",
		})
		return
	}
	linkPantryItem(r, s.store, hh.ID, pi)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "have": true, "pantry": "added",
		"message": fmt.Sprintf("Added %.4g %s of %s to your pantry.", qty, unit, line.DisplayName),
	})
}

// shoppingLineByID finds one line on the household's current plan. Scoped
// through the plan rather than looked up by id alone, so a line id from
// another household resolves to nothing.
func (s *Server) shoppingLineByID(ctx context.Context, householdID, id int64) *db.ShoppingListItem {
	p, _ := s.store.GetLatestPlan(ctx, householdID)
	if p == nil {
		return nil
	}
	lines, _ := s.store.ListShoppingListItems(ctx, p.ID)
	for _, ln := range lines {
		if ln.ID == id {
			return ln
		}
	}
	return nil
}

// ── Manual price editor (pencil icon on the shopping list) ──────────────────

type shoppingPriceStoreOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type shoppingPriceHistoryRow struct {
	PriceLabel string `json:"price_label"`
	RecordedBy string `json:"recorded_by"`
	RecordedAt string `json:"recorded_at"`
}

type shoppingPriceModalResp struct {
	OK               bool                       `json:"ok"`
	Error            string                     `json:"error,omitempty"`
	DisplayName      string                     `json:"display_name"`
	StoreID          int64                      `json:"store_id"`
	Stores           []shoppingPriceStoreOption `json:"stores"`
	PriceDollars     string                     `json:"price_dollars"`
	AmountPerPackage float64                    `json:"amount_per_package"`
	PurchaseUnit     string                     `json:"purchase_unit"`
	History          []shoppingPriceHistoryRow  `json:"history"`
}

// loadOwnedShoppingListItem returns the shopping-list line for id, or nil when
// it doesn't exist or its plan belongs to a different household.
func (s *Server) loadOwnedShoppingListItem(ctx context.Context, hh *db.Household, id int64) (*db.ShoppingListItem, error) {
	line, err := s.store.GetShoppingListItem(ctx, id)
	if err != nil || line == nil {
		return nil, err
	}
	plan, err := s.store.GetPlanByID(ctx, line.PlanID)
	if err != nil || plan == nil || plan.HouseholdID != hh.ID {
		return nil, err
	}
	return line, nil
}

// resolveLineItemID returns the catalog item already linked to a shopping list
// line, falling back to a term lookup for lines priced before items existed.
func (s *Server) resolveLineItemID(ctx context.Context, hh *db.Household, line *db.ShoppingListItem) int64 {
	if line.ItemID != nil {
		return *line.ItemID
	}
	if it, _ := s.store.GetItemByTerm(ctx, hh.ID, pricing.Normalize(line.DisplayName)); it != nil {
		return it.ID
	}
	return 0
}

// handleShoppingItemPriceGet serves the pencil-icon modal's current values plus
// recent price_history for the (item, store) pair.
func (s *Server) handleShoppingItemPriceGet(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, shoppingPriceModalResp{Error: "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, shoppingPriceModalResp{Error: "bad id"})
		return
	}
	ctx := r.Context()
	line, err := s.loadOwnedShoppingListItem(ctx, hh, id)
	if err != nil || line == nil {
		writeJSON(w, http.StatusNotFound, shoppingPriceModalResp{Error: "not found"})
		return
	}

	stores, _ := s.store.ListStores(ctx, hh.ID)
	resp := shoppingPriceModalResp{
		OK:           true,
		DisplayName:  line.DisplayName,
		PurchaseUnit: line.PurchaseUnit,
	}
	for _, gs := range stores {
		resp.Stores = append(resp.Stores, shoppingPriceStoreOption{ID: gs.ID, Name: gs.Name})
	}
	switch {
	case line.StoreID != nil:
		resp.StoreID = *line.StoreID
	case len(stores) > 0:
		resp.StoreID = stores[0].ID
	}

	if itemID := s.resolveLineItemID(ctx, hh, line); itemID != 0 && resp.StoreID != 0 {
		if pkg, _ := s.store.GetItemStorePackage(ctx, itemID, resp.StoreID); pkg != nil {
			resp.PriceDollars = fmt.Sprintf("%.2f", float64(pkg.PriceCents)/100)
			resp.AmountPerPackage = pkg.AmountPerPackage
			resp.PurchaseUnit = pkg.PurchaseUnit
		}
		hist, _ := s.store.ListPriceHistory(ctx, itemID, resp.StoreID)
		for _, h := range hist {
			resp.History = append(resp.History, shoppingPriceHistoryRow{
				PriceLabel: fmt.Sprintf("$%.2f / %s", float64(h.PriceCents)/100, h.PurchaseUnit),
				RecordedBy: h.RecordedBy,
				RecordedAt: h.RecordedAt.Format("Jan 2, 2006 3:04pm"),
			})
		}
	}
	if resp.AmountPerPackage <= 0 {
		if line.PackSize > 0 {
			resp.AmountPerPackage = line.PackSize
		} else {
			resp.AmountPerPackage = 1
		}
	}
	if resp.PriceDollars == "" && line.UnitPriceCents > 0 {
		resp.PriceDollars = fmt.Sprintf("%.2f", float64(line.UnitPriceCents)/100)
	}
	if resp.PurchaseUnit == "" {
		resp.PurchaseUnit = "each"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleShoppingItemPriceSet saves a manually entered price for the item this
// line resolves to (creating the catalog item on first use), records it to
// price_history via UpsertItemStorePackage, and patches the line itself so the
// shopping list reflects it without a full re-price of the plan.
func (s *Server) handleShoppingItemPriceSet(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	ctx := r.Context()
	line, err := s.loadOwnedShoppingListItem(ctx, hh, id)
	if err != nil || line == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}

	var body struct {
		StoreID          int64   `json:"store_id"`
		PriceDollars     string  `json:"price_dollars"`
		AmountPerPackage float64 `json:"amount_per_package"`
		PurchaseUnit     string  `json:"purchase_unit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
		return
	}
	if body.StoreID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "choose a store"})
		return
	}
	priceCents := parseDollarsToCents(body.PriceDollars)
	if priceCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "enter a positive price"})
		return
	}
	amt := body.AmountPerPackage
	if amt <= 0 {
		amt = 1
	}
	unit := strings.TrimSpace(body.PurchaseUnit)
	if unit == "" {
		unit = "each"
	}

	item, err := s.store.GetItem(ctx, s.resolveLineItemID(ctx, hh, line))
	if err != nil || item == nil {
		item, err = catalog.EnsureItem(ctx, s.store, hh.ID, line.DisplayName)
	}
	if err != nil || item == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not resolve item"})
		return
	}

	user := ""
	if u := middleware.UserFromCtx(r); u != nil {
		user = u.Username
	}
	if err := s.store.UpsertItemStorePackage(ctx, db.UpsertItemStorePackageParams{
		ItemID:           item.ID,
		StoreID:          body.StoreID,
		PurchaseUnit:     unit,
		AmountPerPackage: amt,
		PriceCents:       priceCents,
		UpdatedBy:        user,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not save price"})
		return
	}

	// Re-derive this line's pack size in the item's stock unit so future
	// display stays consistent with the costing chain's own math (§6.4).
	conv, _ := s.store.ListConversionsForItem(ctx, item.ID)
	packSize, ok := pricing.Convert(amt, unit, item.StockUnit, conv)
	if !ok || packSize <= 0 {
		packSize = amt
	}
	needed := line.BuyQuantity
	if needed <= 0 {
		needed = packSize
	}
	packs := pricing.PacksNeeded(needed, packSize)

	storeID := body.StoreID
	itemID := item.ID
	if err := s.store.UpdateShoppingListItemPrice(ctx, db.UpdateShoppingListItemPriceParams{
		ID:             line.ID,
		StoreID:        &storeID,
		ItemID:         &itemID,
		BuyQuantity:    float64(packs) * packSize,
		PackSize:       packSize,
		PurchaseUnit:   unit,
		UnitPriceCents: priceCents,
		LineTotalCents: priceCents * int64(packs),
		PriceSource:    "manual",
		Confidence:     pricing.ConfidenceManual,
		PantryQtyUsed:  line.PantryQtyUsed,
		InPantry:       line.InPantry,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not update shopping list line"})
		return
	}

	updated, _ := s.store.GetShoppingListItem(ctx, line.ID)
	storeName := ""
	if stores, _ := s.store.ListStores(ctx, hh.ID); stores != nil {
		for _, gs := range stores {
			if gs.ID == body.StoreID {
				storeName = gs.Name
				break
			}
		}
	}
	resp := map[string]any{"ok": true, "store_name": storeName}
	if updated != nil {
		mealTitles, _ := s.store.ListMealTitlesByIngredientID(ctx, updated.PlanID)
		// Only this line's own item is needed here, not the whole catalog.
		itemsByID := map[int64]*db.Item{}
		if updated.ItemID != nil {
			if it, _ := s.store.GetItem(ctx, *updated.ItemID); it != nil {
				itemsByID[it.ID] = it
			}
		}
		resp["line"] = buildLineItem(updated, mealTitles, itemsByID, s.priceVerdicts(ctx, []*db.ShoppingListItem{updated}))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleShoppingAICostAnalysis prices every zero-cost line on the current plan
// via the AI estimate provider directly (not the full resolution chain - the
// button is specifically "ask the AI", not "try every provider again"), then
// recomputes the plan total and confidence summary from all lines.
func (s *Server) handleShoppingAICostAnalysis(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	if s.gen == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no AI provider configured"})
		return
	}
	ctx := r.Context()
	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no plan"})
		return
	}
	lines, _ := s.store.ListShoppingListItems(ctx, p.ID)
	ai := pricing.NewAIEstimateProvider(s.gen, hh.ZIPCode)

	updated := 0
	for _, ln := range lines {
		if ln.LineTotalCents > 0 {
			continue
		}
		term := ""
		if ln.ItemID != nil {
			if it, _ := s.store.GetItem(ctx, *ln.ItemID); it != nil {
				term = it.NormalizedTerm
			}
		}
		if term == "" {
			term = pricing.Normalize(ln.DisplayName)
		}
		if term == "" {
			continue
		}

		res, err := ai.Lookup(ctx, term, 0, hh.ZIPCode)
		if err != nil || res == nil {
			continue
		}
		needed := ln.BuyQuantity
		if needed <= 0 {
			needed = res.PackSize
		}
		packs := pricing.PacksNeeded(needed, res.PackSize)

		if err := s.store.UpdateShoppingListItemPrice(ctx, db.UpdateShoppingListItemPriceParams{
			ID:             ln.ID,
			StoreID:        ln.StoreID,
			ItemID:         ln.ItemID,
			BuyQuantity:    float64(packs) * res.PackSize,
			PackSize:       res.PackSize,
			PurchaseUnit:   res.PurchaseUnit,
			UnitPriceCents: res.PriceCents,
			LineTotalCents: res.PriceCents * int64(packs),
			PriceSource:    res.Source,
			Confidence:     res.Confidence,
		}); err == nil {
			updated++
		}
	}

	// Recompute the plan total and confidence summary from every line, not
	// just the ones this pass touched.
	lines, _ = s.store.ListShoppingListItems(ctx, p.ID)
	var total int64
	counts := map[string]int{}
	for _, ln := range lines {
		total += ln.LineTotalCents
		counts[ln.Confidence]++
	}
	summary := pricing.BuildConfidenceSummary(counts, len(lines))
	_ = s.store.UpdatePlanTotal(ctx, p.ID, total, summary)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated})
}
