package web

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goeat/catalog"
	"goeat/db"
	"goeat/items"
	"goeat/middleware"
	"goeat/pricing"
)

// itemImageURL is the <img src> for an item: the stored file, or the shared
// placeholder when there is no photo yet.
func itemImageURL(it *db.Item) string {
	if it != nil && it.ImagePath != "" {
		return "/item-images/" + it.ImagePath
	}
	return "/static/img/items/placeholder.svg"
}

func parseDollarsToCents(s string) int64 {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(math.Round(f * 100))
}

// handleItemImageServe serves catalog-item images from the runtime itemImageDir.
func (s *Server) handleItemImageServe(w http.ResponseWriter, r *http.Request) {
	if s.itemImageDir == "" {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, s.itemImageDir+"/"+name)
}

// itemImageFetching tracks item IDs with an in-flight lazy download so a burst
// of page views does not fire the same fetch many times. itemImageFailed records
// the last failure time per item so a dead source URL is not retried on every
// single page load (freefoodphotos.com throttles bursts hard).
var (
	itemImageFetching sync.Map            // int64 -> struct{}
	itemImageFailed   sync.Map            // int64 -> time.Time
	itemImageGate     = make(chan int, 1) // 1 lazy download at a time
	itemImageLastAt   atomic.Int64        // unix-nano of the last download start
)

const (
	itemImageRetryAfter  = 15 * time.Minute
	itemImageMinInterval = 2 * time.Second // spacing between lazy downloads
)

// lazyFetchItemImage kicks off a one-shot background download for an item that
// has a source URL but no stored image yet. It returns immediately; the next
// page load picks up the cached file. A fetch that fails is not retried for
// itemImageRetryAfter.
func (s *Server) lazyFetchItemImage(it *db.Item) {
	if it == nil || s.itemImageDir == "" || it.ImagePath != "" || it.ImageSourceURL == "" {
		return
	}
	if t, failed := itemImageFailed.Load(it.ID); failed && time.Since(t.(time.Time)) < itemImageRetryAfter {
		return
	}
	if _, busy := itemImageFetching.LoadOrStore(it.ID, struct{}{}); busy {
		return
	}
	go func(it db.Item) {
		defer itemImageFetching.Delete(it.ID)

		// Serialize lazy downloads and space them out so a page full of new
		// items does not fan out into a burst the source host will throttle.
		itemImageGate <- 1
		defer func() { <-itemImageGate }()
		if last := itemImageLastAt.Load(); last != 0 {
			if wait := itemImageMinInterval - time.Since(time.Unix(0, last)); wait > 0 {
				time.Sleep(wait)
			}
		}
		itemImageLastAt.Store(time.Now().UnixNano())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		name, err := items.DownloadImage(ctx, it.ImageSourceURL, s.itemImageDir)
		if err != nil {
			itemImageFailed.Store(it.ID, time.Now())
			log.Printf("item image: fetch %d (%s): %v", it.ID, it.ImageSourceURL, err)
			return
		}
		itemImageFailed.Delete(it.ID)
		if err := s.store.SetItemImage(ctx, it.ID, name, it.ImageAttribution); err != nil {
			log.Printf("item image: save %d: %v", it.ID, err)
		}
	}(*it)
}

// ── Attributions page ────────────────────────────────────────────────────────

type attributionRow struct {
	Name string
	Text string
}

type attributionsPageData struct {
	Rows []attributionRow
}

// handleAttributionsPage lists the credit lines required by the sources of
// catalog-item photos (e.g. freefoodphotos.com's image licence). Linked from
// the site footer.
func (s *Server) handleAttributionsPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	var rows []attributionRow
	if hh != nil {
		list, _ := s.store.ListItems(r.Context(), hh.ID)
		seen := map[string]bool{}
		for _, it := range list {
			text := strings.TrimSpace(it.ImageAttribution)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			rows = append(rows, attributionRow{Name: it.Name, Text: text})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	}
	s.render(w, r, "attributions", attributionsPageData{Rows: rows})
}

// ── Items catalog UI (sub-tab of Pantry) ─────────────────────────────────────

type itemRow struct {
	*db.Item
	ImageURL string
}

type itemsPageData struct {
	Items      []itemRow
	Categories []string
	FilterQ    string
	FilterCat  string
	Filtered   bool
	Page       Pagination
}

func (s *Server) handleItemsPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cat := strings.TrimSpace(r.URL.Query().Get("category"))

	all, _ := s.store.ListItems(ctx, hh.ID)
	catSet := map[string]bool{}
	for _, it := range all {
		if it.Category != "" {
			catSet[it.Category] = true
		}
	}
	cats := make([]string, 0, len(catSet))
	for c := range catSet {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	list, _ := s.store.FilterItems(ctx, hh.ID, db.ItemFilter{Q: q, Category: cat})
	rows := make([]itemRow, 0, len(list))
	for _, it := range list {
		s.lazyFetchItemImage(it)
		rows = append(rows, itemRow{Item: it, ImageURL: itemImageURL(it)})
	}
	rows, page := paginate(r, rows)

	s.render(w, r, "items", itemsPageData{
		Items:      rows,
		Categories: cats,
		FilterQ:    q,
		FilterCat:  cat,
		Filtered:   q != "" || cat != "",
		Page:       page,
	})
}

type itemPackageRow struct {
	*db.ItemStorePackage
	StoreName  string
	PriceLabel string
}

type itemDetailPageData struct {
	Item        *db.Item
	ImageURL    string
	Stores      []*db.GroceryStore
	Packages    []itemPackageRow
	Conversions []*db.UnitConversion // hand-entered item bridges (editable)
	Derived     []*db.UnitConversion // auto-precomputed edges to the stock unit
	UnitOptions []string

	// PriceCharts is one series per store that has a price history for this
	// item. Per store rather than one combined line: two shops' prices for the
	// same thing are not the same measurement, and averaging them would draw a
	// trend that no shelf anywhere ever had.
	PriceCharts []itemPriceChart
}

// itemPriceChart is one store's price history for an item.
type itemPriceChart struct {
	StoreName string
	Unit      string
	Spark     Sparkline
}

var commonUnits = []string{
	"each", "g", "kg", "oz", "lb", "ml", "l", "tsp", "tbsp", "cup", "fl-oz",
	"can", "package", "bottle", "jar", "bag", "box", "bunch", "head", "clove",
	"slice", "stick", "stalk", "loaf", "dozen",
}

// unitOptionsFor returns the unit picker list with the item's own stock unit
// guaranteed present (and first) so an unusual stock unit still shows selected.
func unitOptionsFor(stockUnit string) []string {
	su := strings.TrimSpace(stockUnit)
	if su == "" {
		return commonUnits
	}
	for _, u := range commonUnits {
		if u == su {
			return commonUnits
		}
	}
	return append([]string{su}, commonUnits...)
}

func (s *Server) handleItemDetail(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	it, err := s.store.GetItem(ctx, id)
	if err != nil || it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	s.lazyFetchItemImage(it)

	stores, _ := s.store.ListStores(ctx, hh.ID)
	storeName := map[int64]string{}
	for _, gs := range stores {
		storeName[gs.ID] = gs.Name
	}
	pkgs, _ := s.store.ListPackagesForItem(ctx, id)
	pkgRows := make([]itemPackageRow, 0, len(pkgs))
	for _, p := range pkgs {
		pkgRows = append(pkgRows, itemPackageRow{
			ItemStorePackage: p,
			StoreName:        storeName[p.StoreID],
			PriceLabel:       fmt.Sprintf("$%.2f", float64(p.PriceCents)/100),
		})
	}

	convAll, _ := s.store.ListConversionsForItem(ctx, id)
	var conv, derived []*db.UnitConversion
	for _, c := range convAll {
		if c.ItemID == nil {
			continue
		}
		if c.Derived {
			derived = append(derived, c)
		} else {
			conv = append(conv, c)
		}
	}

	s.render(w, r, "item_detail", itemDetailPageData{
		Item:        it,
		ImageURL:    itemImageURL(it),
		Stores:      stores,
		Packages:    pkgRows,
		Conversions: conv,
		Derived:     derived,
		UnitOptions: unitOptionsFor(it.StockUnit),
		PriceCharts: s.itemPriceCharts(ctx, it, stores),
	})
}

// itemPriceCharts builds one sparkline per store from the item's recorded
// price history.
//
// price_history has been filling up since 00014 and, until now, only one modal
// ever read it. Prices are normalised to cents per unit of amount before
// charting, so a 2 lb pack and a 5 lb pack of the same thing sit on the same
// scale instead of drawing a cliff every time the pack size changed.
func (s *Server) itemPriceCharts(ctx context.Context, it *db.Item, stores []*db.GroceryStore) []itemPriceChart {
	entries, err := s.store.ListPriceHistoryForItem(ctx, it.ID)
	if err != nil || len(entries) == 0 {
		return nil
	}

	names := make(map[int64]string, len(stores))
	for _, st := range stores {
		names[st.ID] = st.Name
	}

	type series struct {
		pts  []SparkPoint
		unit string
	}
	byStore := map[int64]*series{}
	var order []int64
	for _, e := range entries {
		sr, ok := byStore[e.StoreID]
		if !ok {
			sr = &series{unit: e.PurchaseUnit}
			byStore[e.StoreID] = sr
			order = append(order, e.StoreID)
		}
		sr.pts = append(sr.pts, SparkPoint{
			Cents: unitPrice(e.PriceCents, e.AmountPerPackage),
			At:    e.RecordedAt,
		})
	}

	out := make([]itemPriceChart, 0, len(order))
	for _, sid := range order {
		sr := byStore[sid]
		spark, ok := BuildSparkline(sr.pts, 260, 56)
		if !ok {
			continue // one reading is a number, not a trend
		}
		name := names[sid]
		if name == "" {
			// The store was deleted since the price was recorded. The history
			// is still true, so it is shown rather than dropped.
			name = "a store you no longer shop at"
		}
		out = append(out, itemPriceChart{StoreName: name, Unit: sr.unit, Spark: spark})
	}
	return out
}

func (s *Server) handleItemCreate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.setNotify(w, NotifyDanger, "Item name is required.")
		http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
		return
	}
	term := pricing.Normalize(name)
	if existing, _ := s.store.GetItemByTerm(r.Context(), hh.ID, term); existing != nil {
		s.setNotify(w, NotifyInfo, fmt.Sprintf("%q already maps to an existing item.", name))
		http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", existing.ID), http.StatusSeeOther)
		return
	}
	unit := strings.TrimSpace(r.FormValue("stock_unit"))
	if unit == "" {
		unit = "each"
	}
	qty, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("default_purchase_qty")), 64)
	if qty <= 0 {
		qty = 1
	}
	it, err := s.store.CreateItem(r.Context(), db.CreateItemParams{
		HouseholdID:        hh.ID,
		Name:               name,
		NormalizedTerm:     term,
		Category:           strings.TrimSpace(r.FormValue("category")),
		StockUnit:          unit,
		DefaultPurchaseQty: qty,
		Source:             "manual",
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Could not create item: %v", err))
		http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
		return
	}
	_ = catalog.RecalcItemConversions(r.Context(), s.store, it.ID)
	s.setNotify(w, NotifySuccess, fmt.Sprintf("%q added to the catalog.", name))
	http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", it.ID), http.StatusSeeOther)
}

func (s *Server) handleItemEdit(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	it, err := s.store.GetItem(r.Context(), id)
	if err != nil || it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = it.Name
	}
	unit := strings.TrimSpace(r.FormValue("stock_unit"))
	if unit == "" {
		unit = it.StockUnit
	}
	qty, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("default_purchase_qty")), 64)
	if qty <= 0 {
		qty = it.DefaultPurchaseQty
	}
	err = s.store.UpdateItem(r.Context(), db.UpdateItemParams{
		ID:                 id,
		Name:               name,
		Category:           strings.TrimSpace(r.FormValue("category")),
		StockUnit:          unit,
		DefaultPurchaseQty: qty,
		Notes:              strings.TrimSpace(r.FormValue("notes")),
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, "Could not save changes.")
	} else {
		_ = catalog.RecalcItemConversions(r.Context(), s.store, id)
		s.setNotify(w, NotifySuccess, "Item updated.")
	}
	http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", id), http.StatusSeeOther)
}

func (s *Server) handleItemDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	it, _ := s.store.GetItem(r.Context(), id)
	if it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteItem(r.Context(), id); err != nil {
		s.setNotify(w, NotifyDanger, "Could not delete item.")
		http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", id), http.StatusSeeOther)
		return
	}
	items.RemoveImage(s.itemImageDir, it.ImagePath)
	s.setNotify(w, NotifySuccess, fmt.Sprintf("%q removed from the catalog.", it.Name))
	http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
}

func (s *Server) handleItemImageReplace(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	dest := fmt.Sprintf("/pantry/items/%d", id)
	it, err := s.store.GetItem(ctx, id)
	if err != nil || it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil && r.MultipartForm == nil {
		_ = r.ParseForm()
	}

	if r.FormValue("remove") == "1" {
		if err := s.store.ClearItemImage(ctx, id); err != nil {
			s.setNotify(w, NotifyDanger, "Could not remove image.")
		} else {
			items.RemoveImage(s.itemImageDir, it.ImagePath)
			s.setNotify(w, NotifySuccess, "Image removed.")
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}

	var newName string
	if f, fh, ferr := r.FormFile("image_file"); ferr == nil {
		defer f.Close()
		data, rerr := io.ReadAll(io.LimitReader(f, 8<<20))
		if rerr != nil {
			s.setNotify(w, NotifyDanger, "Could not read the uploaded file.")
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		newName, err = items.SaveImageBytes(s.itemImageDir, fh.Filename, data)
	} else if raw := strings.TrimSpace(r.FormValue("image_url")); raw != "" {
		newName, err = items.DownloadImage(ctx, raw, s.itemImageDir)
	} else {
		s.setNotify(w, NotifyDanger, "Choose a file or paste an image URL.")
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Image failed: %v", err))
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	attribution := strings.TrimSpace(r.FormValue("attribution"))
	if err := s.store.SetItemImage(ctx, id, newName, attribution); err != nil {
		s.setNotify(w, NotifyDanger, "Could not save the new image.")
		items.RemoveImage(s.itemImageDir, newName)
	} else {
		if it.ImagePath != "" && it.ImagePath != newName {
			items.RemoveImage(s.itemImageDir, it.ImagePath)
		}
		s.setNotify(w, NotifySuccess, "Image updated.")
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleItemPackageUpsert(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	dest := fmt.Sprintf("/pantry/items/%d", id)
	it, _ := s.store.GetItem(r.Context(), id)
	if it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	storeID, _ := strconv.ParseInt(r.FormValue("store_id"), 10, 64)
	if storeID == 0 {
		s.setNotify(w, NotifyDanger, "Choose a store.")
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	unit := strings.TrimSpace(r.FormValue("purchase_unit"))
	if unit == "" {
		unit = "each"
	}
	amt, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount_per_package")), 64)
	if amt <= 0 {
		amt = 1
	}
	user := ""
	if u := middleware.UserFromCtx(r); u != nil {
		user = u.Username
	}
	err = s.store.UpsertItemStorePackage(r.Context(), db.UpsertItemStorePackageParams{
		ItemID:           id,
		StoreID:          storeID,
		PurchaseUnit:     unit,
		AmountPerPackage: amt,
		PriceCents:       parseDollarsToCents(r.FormValue("price_dollars")),
		UpdatedBy:        user,
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, "Could not save the store package.")
	} else {
		s.setNotify(w, NotifySuccess, "Store package saved.")
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleItemPackageDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	pkgID, err := strconv.ParseInt(r.PathValue("pkgID"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	it, _ := s.store.GetItem(r.Context(), id)
	if it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	_ = s.store.DeleteItemStorePackage(r.Context(), pkgID)
	http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", id), http.StatusSeeOther)
}

func (s *Server) handleItemConversionUpsert(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	dest := fmt.Sprintf("/pantry/items/%d", id)
	it, _ := s.store.GetItem(r.Context(), id)
	if it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	from := strings.TrimSpace(r.FormValue("from_unit"))
	to := strings.TrimSpace(r.FormValue("to_unit"))
	factor, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("factor")), 64)
	if from == "" || to == "" || factor <= 0 {
		s.setNotify(w, NotifyDanger, "Enter both units and a positive factor.")
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	if err := s.store.UpsertUnitConversion(r.Context(), db.UpsertUnitConversionParams{
		ItemID: &id, FromUnit: from, ToUnit: to, Factor: factor,
	}); err != nil {
		s.setNotify(w, NotifyDanger, "Could not save conversion.")
	} else {
		_ = catalog.RecalcItemConversions(r.Context(), s.store, id)
		s.setNotify(w, NotifySuccess, fmt.Sprintf("1 %s = %g %s saved.", from, factor, to))
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleItemConversionDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	it, _ := s.store.GetItem(r.Context(), id)
	if it == nil || it.HouseholdID != hh.ID {
		http.NotFound(w, r)
		return
	}
	_ = s.store.DeleteUnitConversion(r.Context(), cid)
	_ = catalog.RecalcItemConversions(r.Context(), s.store, id)
	http.Redirect(w, r, fmt.Sprintf("/pantry/items/%d", id), http.StatusSeeOther)
}
