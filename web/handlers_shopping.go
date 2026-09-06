package web

import (
	"fmt"
	"net/http"
	"strconv"

	"goeat/db"
	"goeat/middleware"
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
}

func (s *Server) handleShoppingListPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil || p.Status == "generating" {
		s.render(w, r, "shopping_list", shoppingListPageData{HasPlan: false})
		return
	}

	rawItems, _ := s.store.ListShoppingListItems(ctx, p.ID)
	stores, _ := s.store.ListStores(ctx, hh.ID)

	storeMap := make(map[int64]string, len(stores))
	for _, gs := range stores {
		storeMap[gs.ID] = gs.Name
	}

	// Group items by store.
	groupIndex := make(map[int64]int)
	var groups []shoppingStoreGroup
	var unassigned []shoppingLineItem

	for _, item := range rawItems {
		line := buildLineItem(item)
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

	// Compute per-store subtotals.
	for i := range groups {
		var sub int64
		for _, item := range groups[i].Items {
			// Re-derive cents from the label for display only - avoid re-querying.
			// The subtotal is computed from the raw items below instead.
			_ = item
		}
		_ = sub
	}
	// Compute subtotals properly from raw items.
	storeSubs := make(map[int64]int64)
	for _, item := range rawItems {
		if item.StoreID != nil {
			storeSubs[*item.StoreID] += item.LineTotalCents
		}
	}
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

	s.render(w, r, "shopping_list", shoppingListPageData{
		HasPlan:         true,
		PlanID:          p.ID,
		WeekStart:       p.WeekStart,
		TotalLabel:      fmt.Sprintf("$%.2f", float64(p.TotalCents)/100),
		BudgetLabel:     fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100),
		OverBudget:      p.TotalCents > p.BudgetCents && p.TotalCents > 0,
		Groups:          groups,
		UnassignedItems: unassigned,
	})
}

func buildLineItem(item *db.ShoppingListItem) shoppingLineItem {
	buyLabel := fmt.Sprintf("%.4g %s", item.BuyQuantity, item.PurchaseUnit)
	priceLabel := ""
	if item.UnitPriceCents > 0 {
		priceLabel = fmt.Sprintf("$%.2f / %s", float64(item.UnitPriceCents)/100, item.PurchaseUnit)
	}
	totalLabel := fmt.Sprintf("$%.2f", float64(item.LineTotalCents)/100)

	badgeClass, badgeText := confidenceBadge(item.Confidence)

	return shoppingLineItem{
		ID:          item.ID,
		DisplayName: item.DisplayName,
		BuyLabel:    buyLabel,
		PriceLabel:  priceLabel,
		TotalLabel:  totalLabel,
		BadgeClass:  badgeClass,
		BadgeText:   badgeText,
		Checked:     item.Checked,
	}
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
