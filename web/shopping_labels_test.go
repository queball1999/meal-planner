package web

import (
	"testing"

	"goeat/db"
)

// Rows as they were stored on a real list: quantities in the item's stock
// unit, rounded up to whole store packs. The list must read as what the
// recipes need, with the packs and the per-pack price beside it.
func TestShoppingLineLabels(t *testing.T) {
	cases := []struct {
		name               string
		item               db.ShoppingListItem
		stockUnit          string
		wantNeed, wantPack string
		wantPrice          string
	}{
		{
			name: "bananas sold by the pound",
			item: db.ShoppingListItem{NeedQuantity: 4, BuyQuantity: 7.688006271186441, PackSize: 3.8440031355932205,
				PackAmount: 1, PackUnit: "lb", PurchaseUnit: "lb", UnitPriceCents: 55},
			stockUnit: "each", wantNeed: "4", wantPack: "buy 2 × 1 lb", wantPrice: "$0.55 / lb",
		},
		{
			name: "yogurt in a 32 oz tub",
			item: db.ShoppingListItem{NeedQuantity: 340.19, BuyQuantity: 907.1847412751217, PackSize: 907.1847412751217,
				PackAmount: 32, PackUnit: "oz", PurchaseUnit: "oz", UnitPriceCents: 469},
			stockUnit: "g", wantNeed: "340.19 g", wantPack: "buy 1 × 32 oz", wantPrice: "$4.69 / 32 oz",
		},
		{
			name: "chicken priced per 9 oz pack, not per ounce",
			item: db.ShoppingListItem{NeedQuantity: 141.75, BuyQuantity: 255.14570848362797, PackSize: 255.14570848362797,
				PackAmount: 9, PackUnit: "oz", PurchaseUnit: "oz", UnitPriceCents: 449},
			stockUnit: "g", wantNeed: "141.75 g", wantPack: "buy 1 × 9 oz", wantPrice: "$4.49 / 9 oz",
		},
		{
			name: "eggs by the 18-pack",
			item: db.ShoppingListItem{NeedQuantity: 12, BuyQuantity: 18, PackSize: 18,
				PackAmount: 18, PackUnit: "each", PurchaseUnit: "each", UnitPriceCents: 265},
			stockUnit: "each", wantNeed: "12", wantPack: "buy 1 pack of 18", wantPrice: "$2.65 / pack of 18",
		},
		{
			name: "cans bought one per pack",
			item: db.ShoppingListItem{NeedQuantity: 2, BuyQuantity: 2, PackSize: 1,
				PackAmount: 1, PackUnit: "can", PurchaseUnit: "can", UnitPriceCents: 100},
			stockUnit: "can", wantNeed: "2 cans", wantPack: "buy 2 × 1 can", wantPrice: "$1.00 / can",
		},
		{
			name:      "never matched to a package",
			item:      db.ShoppingListItem{NeedQuantity: 0, BuyQuantity: 0.5, PurchaseUnit: "each", UnitPriceCents: 119},
			stockUnit: "each", wantNeed: "0.5", wantPack: "", wantPrice: "$1.19 / each",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			need := lineNeed(&c.item)
			if got := displayQtyLabel(need, c.stockUnit, ""); got != c.wantNeed {
				t.Errorf("need = %q, want %q", got, c.wantNeed)
			}
			if got := linePackLabel(&c.item, need, c.stockUnit); got != c.wantPack {
				t.Errorf("pack = %q, want %q", got, c.wantPack)
			}
			if got := linePriceLabel(&c.item); got != c.wantPrice {
				t.Errorf("price = %q, want %q", got, c.wantPrice)
			}
		})
	}
}
