package web

import "testing"

func TestQtyLabel(t *testing.T) {
	cases := []struct {
		qty  float64
		unit string
		want string
	}{
		{4, "each", "4"},
		{2, "", "2"},
		{1, "count", "1"},
		{3, "pcs", "3"},
		{2, "slice", "2 slices"},
		{1, "slice", "1 slice"},
		{2, "clove", "2 cloves"},
		{1.5, "kg", "1.5 kg"},
		{680.4, "g", "680.4 g"},
		{907.1847412751217, "g", "907.18 g"},
		{0.3333, "cup", "0.33 cups"},
		{0.5, "cup", "0.5 cups"},
		{2, "cartons", "2 cartons"},
		{1, "carton", "1 carton"},
		{0, "each", ""},
		{0, "lb", "lb"},
		{12, "tbsp", "12 tbsp"},
	}
	for _, c := range cases {
		if got := qtyLabel(c.qty, c.unit); got != c.want {
			t.Errorf("qtyLabel(%v, %q) = %q, want %q", c.qty, c.unit, got, c.want)
		}
	}
}
