package web

import "testing"

func TestStateForZIP(t *testing.T) {
	cases := map[string]string{
		"85260": "AZ", // Scottsdale
		"85001": "AZ",
		"33101": "FL",
		"10001": "NY",
		"02139": "MA",
		"99501": "AK",
		"96813": "HI",
		"79936": "TX",
		"88510": "TX", // El Paso's out-of-sequence block
		"abcde": "",
		"":      "",
		"1234":  "",
	}
	for zip, want := range cases {
		if got := StateForZIP(zip); got != want {
			t.Errorf("StateForZIP(%q) = %q, want %q", zip, got, want)
		}
	}
}

func TestStoreAvailability(t *testing.T) {
	publix := KnownStoreByName("Publix")
	if publix == nil {
		t.Fatal("Publix missing from catalog")
	}
	if publix.AvailableIn("AZ") {
		t.Error("Publix should not be offered in Arizona")
	}
	if !publix.AvailableIn("FL") {
		t.Error("Publix should be offered in Florida")
	}
	// An unplaceable ZIP must not hide the whole catalog.
	if !publix.AvailableIn("") {
		t.Error("unknown state should fall back to available")
	}

	walmart := KnownStoreByName("Walmart")
	if !walmart.AvailableIn("AZ") || !walmart.AvailableIn("VT") {
		t.Error("nationwide chains should be available everywhere")
	}
}

func TestEveryCatalogStoreHasASearchURL(t *testing.T) {
	for _, ks := range KnownStores {
		if ks.SearchURL == "" {
			t.Errorf("%s has no SearchURL", ks.Name)
			continue
		}
		if !contains(ks.SearchURL, "{term}") {
			t.Errorf("%s search URL is missing the {term} placeholder: %s", ks.Name, ks.SearchURL)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
