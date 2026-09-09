package pricing

import (
	"math"
	"testing"
)

func TestDisplayQuantity(t *testing.T) {
	cases := []struct {
		name     string
		qty      float64
		unit     string
		system   string
		wantQty  float64
		wantUnit string
	}{
		// as-is / empty: never touched
		{"as-is passthrough", 1200, "g", SystemAsIs, 1200, "g"},
		{"empty system passthrough", 1200, "g", "", 1200, "g"},

		// countable units: never converted, whatever the system
		{"each stays each", 4, "each", SystemMetric, 4, "each"},
		{"can stays can", 3, "can", SystemImperial, 3, "can"},

		// metric mass: promote once the number reads large
		{"900 g stays g", 900, "g", SystemMetric, 900, "g"},
		{"1200 g -> 1.2 kg", 1200, "g", SystemMetric, 1.2, "kg"},
		{"lb into metric", 2, "lb", SystemMetric, 907.18474, "g"},
		{"5 lb into metric -> kg", 5, "lb", SystemMetric, 2.26796185, "kg"},

		// imperial mass
		{"12 oz stays oz", 12, "oz", SystemImperial, 12, "oz"},
		{"20 oz -> 1.25 lb", 20, "oz", SystemImperial, 1.25, "lb"},
		{"1000 g into imperial -> lb", 1000, "g", SystemImperial, 2.20462262, "lb"},

		// volume
		{"500 ml stays ml", 500, "ml", SystemMetric, 500, "ml"},
		{"1500 ml -> 1.5 l", 1500, "ml", SystemMetric, 1.5, "l"},

		// non-positive: passthrough
		{"zero passthrough", 0, "g", SystemMetric, 0, "g"},

		// unknown unit: passthrough
		{"unknown unit passthrough", 50, "sprig", SystemMetric, 50, "sprig"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotQty, gotUnit := DisplayQuantity(c.qty, c.unit, c.system, nil)
			if gotUnit != c.wantUnit || math.Abs(gotQty-c.wantQty) > 1e-4 {
				t.Errorf("DisplayQuantity(%v, %q, %q) = (%v, %q), want (%v, %q)",
					c.qty, c.unit, c.system, gotQty, gotUnit, c.wantQty, c.wantUnit)
			}
		})
	}
}
