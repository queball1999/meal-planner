package pricing

import (
	"testing"

	"goeat/db"
)

func findEdge(edges []db.UpsertUnitConversionParams, from string) (float64, bool) {
	for _, e := range edges {
		if e.FromUnit == from {
			return e.Factor, true
		}
	}
	return 0, false
}

func TestAutoConversions_MassStockUnit(t *testing.T) {
	// stock unit g, buy qty 454, no item bridges: mass family + package edge.
	edges := AutoConversions("g", 454, nil)

	if f, ok := findEdge(edges, "kg"); !ok || !approx(f, 1000, 1e-6) {
		t.Fatalf("kg->g edge: got %v ok=%v want 1000", f, ok)
	}
	if f, ok := findEdge(edges, "oz"); !ok || !approx(f, 28.3495, 1e-3) {
		t.Fatalf("oz->g edge: got %v ok=%v want ~28.35", f, ok)
	}
	if f, ok := findEdge(edges, "package"); !ok || f != 454 {
		t.Fatalf("package->g edge: got %v ok=%v want 454", f, ok)
	}
	// No volume path to mass without a bridge.
	if _, ok := findEdge(edges, "cup"); ok {
		t.Fatal("cup->g should not exist without an item bridge")
	}
	// Never emits a self-edge.
	if _, ok := findEdge(edges, "g"); ok {
		t.Fatal("g->g self edge must not be emitted")
	}
}

func TestAutoConversions_SeedBridgeUnlocksVolume(t *testing.T) {
	// 1 cup = 120 g bridges the volume family onto the mass stock unit.
	seed := []*db.UnitConversion{{FromUnit: "cup", ToUnit: "g", Factor: 120}}
	edges := AutoConversions("g", 0, seed)

	if f, ok := findEdge(edges, "cup"); !ok || !approx(f, 120, 1e-9) {
		t.Fatalf("cup->g edge: got %v ok=%v want 120", f, ok)
	}
	if f, ok := findEdge(edges, "tbsp"); !ok || !approx(f, 120.0/16, 1e-6) {
		t.Fatalf("tbsp->g edge: got %v ok=%v want ~7.5", f, ok)
	}
	// buyQty 0 => no package edge.
	if _, ok := findEdge(edges, "package"); ok {
		t.Fatal("package edge must not be emitted when buyQty is 0")
	}
}

func TestAutoConversions_CountStockUnit(t *testing.T) {
	// stock unit each, 1 dozen = 12 each is builtin.
	edges := AutoConversions("each", 12, nil)
	if f, ok := findEdge(edges, "dozen"); !ok || f != 12 {
		t.Fatalf("dozen->each edge: got %v ok=%v want 12", f, ok)
	}
	if f, ok := findEdge(edges, "package"); !ok || f != 12 {
		t.Fatalf("package->each edge: got %v ok=%v want 12", f, ok)
	}
}
