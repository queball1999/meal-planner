package pricing

import (
	"sort"

	"goeat/db"
)

// CommonUnits is the canonical set of units the app offers in pickers and
// precomputes conversions against. Kept here (not in package web) so the UI and
// the auto-conversion builder draw from one list.
var CommonUnits = []string{
	"each", "g", "kg", "mg", "oz", "lb",
	"ml", "l", "tsp", "tbsp", "cup", "fl-oz", "pint", "quart", "gallon",
	"can", "package", "bottle", "jar", "bag", "box",
	"bunch", "head", "clove", "slice", "stick", "stalk", "sprig", "loaf", "dozen",
}

// AutoConversions precomputes a direct edge from every unit the app knows about
// to stockUnit, walking the builtin factor graph plus the item's own seed
// edges. The result is a flattened one-hop view meant to be stored as derived
// rows so costing and the item page never have to BFS at read time.
//
// A non-zero buyQty also yields a package -> stockUnit edge (1 package = buyQty
// stockUnit): the item's default buy size expressed in its stock unit. buyQty
// always wins over any graph-derived "package" path.
func AutoConversions(stockUnit string, buyQty float64, seed []*db.UnitConversion) []db.UpsertUnitConversionParams {
	stock := CanonUnit(stockUnit)

	cand := map[string]bool{}
	for _, u := range CommonUnits {
		cand[CanonUnit(u)] = true
	}
	for _, c := range seed {
		if c == nil {
			continue
		}
		cand[CanonUnit(c.FromUnit)] = true
		cand[CanonUnit(c.ToUnit)] = true
	}
	delete(cand, stock)
	delete(cand, "package") // added explicitly below from buyQty

	var out []db.UpsertUnitConversionParams
	for u := range cand {
		v, ok := Convert(1, u, stock, seed)
		if !ok || v <= 0 {
			continue
		}
		out = append(out, db.UpsertUnitConversionParams{FromUnit: u, ToUnit: stock, Factor: v})
	}
	if buyQty > 0 && stock != "package" {
		out = append(out, db.UpsertUnitConversionParams{FromUnit: "package", ToUnit: stock, Factor: buyQty})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FromUnit < out[j].FromUnit })
	return out
}
