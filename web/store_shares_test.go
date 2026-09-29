package web

import "testing"

func TestNormalizeShares(t *testing.T) {
	cases := []struct {
		name   string
		in     map[string]int
		want   map[string]int
		scaled bool
	}{
		{"already 100", map[string]int{"a": 70, "b": 30}, map[string]int{"a": 70, "b": 30}, false},
		{"all zero stays unset", map[string]int{"a": 0, "b": 0}, map[string]int{"a": 0, "b": 0}, false},
		{"under 100 scales up", map[string]int{"a": 35, "b": 15}, map[string]int{"a": 70, "b": 30}, true},
		{"thirds add up exactly", map[string]int{"a": 10, "b": 10, "c": 10}, map[string]int{"a": 34, "b": 33, "c": 33}, true},
		{"out of range clamped", map[string]int{"a": 150, "b": -5}, map[string]int{"a": 100, "b": 0}, false},
	}
	for _, c := range cases {
		got, scaled := normalizeShares(c.in)
		if scaled != c.scaled {
			t.Errorf("%s: scaled = %v, want %v", c.name, scaled, c.scaled)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %d, want %d (got %v)", c.name, k, got[k], v, got)
			}
		}
	}
}
