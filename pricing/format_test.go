package pricing_test

import (
	"testing"

	"goeat/pricing"
)

func TestFormatQty(t *testing.T) {
	cases := map[float64]string{
		907.1847412751217: "907.18",
		4:                 "4",
		1.5:               "1.5",
		0.3333:            "0.33",
		2.005:             "2.01",
		0.001:             "0",
		-0.001:            "0",
		680.4:             "680.4",
	}
	for in, want := range cases {
		if got := pricing.FormatQty(in); got != want {
			t.Errorf("FormatQty(%v) = %q, want %q", in, got, want)
		}
	}
}
