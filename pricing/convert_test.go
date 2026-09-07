package pricing

import (
	"math"
	"testing"

	"goeat/db"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestConvert_SameUnit(t *testing.T) {
	got, ok := Convert(3, "cups", "cup", nil)
	if !ok || got != 3 {
		t.Fatalf("same-unit convert: got %v ok=%v", got, ok)
	}
}

func TestConvert_MassChain(t *testing.T) {
	// 2 lb -> g -> kg
	got, ok := Convert(2, "lb", "kg", nil)
	if !ok || !approx(got, 0.90718474, 1e-6) {
		t.Fatalf("2 lb in kg: got %v ok=%v want ~0.907", got, ok)
	}
	// reverse direction
	got, ok = Convert(1000, "g", "lb", nil)
	if !ok || !approx(got, 2.20462262, 1e-6) {
		t.Fatalf("1000 g in lb: got %v ok=%v want ~2.2046", got, ok)
	}
}

func TestConvert_VolumeChain(t *testing.T) {
	// 1 cup -> tbsp
	got, ok := Convert(1, "cup", "tbsp", nil)
	if !ok || !approx(got, 16, 1e-9) {
		t.Fatalf("1 cup in tbsp: got %v ok=%v want 16", got, ok)
	}
	// 2 tbsp -> tsp
	got, ok = Convert(2, "Tablespoons", "tsp", nil)
	if !ok || !approx(got, 6, 1e-9) {
		t.Fatalf("2 tbsp in tsp: got %v ok=%v want 6", got, ok)
	}
}

func TestConvert_NoPath(t *testing.T) {
	if _, ok := Convert(1, "cup", "lb", nil); ok {
		t.Fatal("volume->mass should have no path without an item bridge")
	}
}

func TestConvert_ItemBridge(t *testing.T) {
	// 1 clove garlic = 3 g  (item-specific edge)
	extra := []*db.UnitConversion{{FromUnit: "clove", ToUnit: "g", Factor: 3}}
	// 4 cloves -> g
	got, ok := Convert(4, "cloves", "g", extra)
	if !ok || !approx(got, 12, 1e-9) {
		t.Fatalf("4 cloves in g: got %v ok=%v want 12", got, ok)
	}
	// bridge then global chain: 4 cloves -> oz
	got, ok = Convert(4, "clove", "oz", extra)
	if !ok || !approx(got, 12*0.0352739619, 1e-6) {
		t.Fatalf("4 cloves in oz: got %v ok=%v", got, ok)
	}
}

func TestConvert_DozenToEach(t *testing.T) {
	got, ok := Convert(2, "dozen", "each", nil)
	if !ok || got != 24 {
		t.Fatalf("2 dozen in each: got %v ok=%v want 24", got, ok)
	}
}
