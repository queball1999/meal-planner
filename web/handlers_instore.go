package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"goeat/db"
	"goeat/middleware"
)

// aisleOrder is the order a shop is walked, not the order a database returns.
//
// Produce first and frozen late is not arbitrary: it is how supermarkets are
// laid out, and it keeps frozen and chilled goods in the trolley for the
// shortest time. The list is matched against `items.category`, whose real
// values come from catalog/seed_items.json - Produce, Bakery, Meat & Seafood,
// Dairy & Eggs, Frozen, Pantry, Snacks & Sweets, Spices, Condiments,
// Beverages.
//
// Lower sorts earlier. Anything unrecognised gets aisleUnknown, which puts it
// last: an item nobody has categorised is the one most likely to be somewhere
// unexpected, and having it lead the list would send a shopper to the wrong
// end of the shop first.
var aisleOrder = []struct {
	Name  string
	Match []string
}{
	{"Produce", []string{"produce", "fruit", "vegetable"}},
	{"Bakery", []string{"bakery", "bread"}},
	{"Deli", []string{"deli"}},
	{"Meat & Seafood", []string{"meat", "seafood", "fish", "poultry"}},
	{"Dairy & Eggs", []string{"dairy", "egg", "cheese"}},
	{"Pantry", []string{"pantry", "canned", "dry goods", "grain", "pasta", "baking"}},
	{"Spices & Condiments", []string{"spice", "condiment", "sauce", "herb"}},
	{"Snacks & Sweets", []string{"snack", "sweet", "candy", "confection"}},
	{"Beverages", []string{"beverage", "drink", "juice", "coffee", "tea"}},
	{"Frozen", []string{"frozen"}},
	{"Household", []string{"household", "cleaning", "paper", "pet"}},
}

// aisleUnknown sorts after every named aisle.
const aisleUnknown = 1000

// aisleFor maps a catalog category onto a walking position.
//
// Substring matching rather than exact: category is free text on the item
// form, so "Fresh Produce", "produce" and "Produce & Herbs" all have to land
// in the same aisle.
//
// "Frozen" is tested before the table because a frozen product names two
// categories at once - "frozen vegetables", "frozen pizza" - and a plain
// walk down the table would file it under whichever word came first in the
// list, which is produce. The freezer is where it actually is.
func aisleFor(category string) (int, string) {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" {
		return aisleUnknown, "Everything else"
	}
	// Frozen is checked ahead of the table because "frozen vegetables" and
	// "frozen pizza" name a second category as well, and the freezer is where
	// they actually are.
	if strings.Contains(c, "frozen") {
		for i, a := range aisleOrder {
			if a.Name == "Frozen" {
				return i, a.Name
			}
		}
	}
	for i, a := range aisleOrder {
		for _, m := range a.Match {
			if strings.Contains(c, m) {
				return i, a.Name
			}
		}
	}
	return aisleUnknown, "Everything else"
}

// instoreLine is one row in the shopping view.
type instoreLine struct {
	ID       int64
	Name     string
	Qty      string
	Price    string
	Checked  bool
	InPantry bool
	Store    string
}

// instoreAisle is one section of the shop.
type instoreAisle struct {
	Name  string
	Lines []instoreLine
	// Remaining is how many of this aisle's lines are still unchecked, so a
	// finished aisle can be collapsed out of the way.
	Remaining int
}

type instorePageData struct {
	HasPlan   bool
	WeekStart string
	Aisles    []instoreAisle
	Total     string
	InCart    string
	Remaining int
	Count     int
}

// handleInStore renders the shopping list for the twenty minutes it exists
// for: walking round a shop.
//
// A separate page rather than a mode toggle on /plan/list. The two views want
// opposite things - the planning list is dense, sortable by store, and full of
// controls for editing prices and matching items; this one wants one item per
// row, targets a thumb can hit while pushing a trolley, and nothing that can
// be tapped by accident.
func (s *Server) handleInStore(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		s.render(w, r, "instore", instorePageData{})
		return
	}

	lines, _ := s.store.ListShoppingListItems(ctx, p.ID)
	stores, _ := s.store.ListStores(ctx, hh.ID)
	storeNames := make(map[int64]string, len(stores))
	for _, st := range stores {
		storeNames[st.ID] = st.Name
	}
	items, _ := s.store.ListItems(ctx, hh.ID)
	catByItem := make(map[int64]string, len(items))
	for _, it := range items {
		catByItem[it.ID] = it.Category
	}

	data := instorePageData{HasPlan: true, WeekStart: p.WeekStart}

	type bucket struct {
		order int
		aisle instoreAisle
	}
	buckets := map[string]*bucket{}
	var totalCents, cartCents int64

	for _, ln := range lines {
		// An already-have line is not being bought, so it has no place on a
		// list you are walking round a shop with.
		if ln.InPantry {
			continue
		}
		category := ""
		if ln.ItemID != nil {
			category = catByItem[*ln.ItemID]
		}
		order, name := aisleFor(category)

		b, ok := buckets[name]
		if !ok {
			b = &bucket{order: order, aisle: instoreAisle{Name: name}}
			buckets[name] = b
		}

		store := ""
		if ln.StoreID != nil {
			store = storeNames[*ln.StoreID]
		}
		b.aisle.Lines = append(b.aisle.Lines, instoreLine{
			ID:      ln.ID,
			Name:    ln.DisplayName,
			Qty:     fmt.Sprintf("%.4g %s", ln.BuyQuantity, ln.PurchaseUnit),
			Price:   fmt.Sprintf("$%.2f", float64(ln.LineTotalCents)/100),
			Checked: ln.Checked,
			Store:   store,
		})
		if !ln.Checked {
			b.aisle.Remaining++
			data.Remaining++
		} else {
			cartCents += ln.LineTotalCents
		}
		totalCents += ln.LineTotalCents
		data.Count++
	}

	ordered := make([]*bucket, 0, len(buckets))
	for _, b := range buckets {
		sort.SliceStable(b.aisle.Lines, func(i, j int) bool {
			return strings.ToLower(b.aisle.Lines[i].Name) < strings.ToLower(b.aisle.Lines[j].Name)
		})
		ordered = append(ordered, b)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	for _, b := range ordered {
		data.Aisles = append(data.Aisles, b.aisle)
	}

	data.Total = fmt.Sprintf("$%.2f", float64(totalCents)/100)
	data.InCart = fmt.Sprintf("$%.2f", float64(cartCents)/100)

	s.render(w, r, "instore", data)
}

// instoreLinesFor is the aisle grouping, exposed for tests without a server.
func instoreLinesFor(lines []*db.ShoppingListItem, catByItem map[int64]string) []string {
	seen := map[string]bool{}
	type ord struct {
		n    int
		name string
	}
	var out []ord
	for _, ln := range lines {
		if ln.InPantry {
			continue
		}
		cat := ""
		if ln.ItemID != nil {
			cat = catByItem[*ln.ItemID]
		}
		n, name := aisleFor(cat)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, ord{n, name})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].n < out[j].n })
	names := make([]string, 0, len(out))
	for _, o := range out {
		names = append(names, o.name)
	}
	return names
}
