package scrape

import "testing"

func TestParseStoreContext(t *testing.T) {
	sc := ParseStoreContext(`{"cookies":{"STORE":"177"},"prewarm":["https://x.test/"],"wait_for":"[data-qa=p]"}`)
	if sc.Cookies["STORE"] != "177" || len(sc.Prewarm) != 1 || sc.WaitFor != "[data-qa=p]" {
		t.Fatalf("parsed = %+v", sc)
	}
	if sc.Empty() {
		t.Error("a populated context reported itself empty")
	}

	// A hand-edited value that no longer parses must degrade to "no context"
	// rather than break the store's pricing.
	for _, raw := range []string{"", "{}", "not json", `{"cookies":`} {
		if got := ParseStoreContext(raw); !got.Empty() {
			t.Errorf("ParseStoreContext(%q) = %+v, want empty", raw, got)
		}
	}
}

func TestContextCookiesCarryClearance(t *testing.T) {
	sc := &StoreContext{Cookies: map[string]string{"STORE": "177", "ZIP": "85716"}}
	cl := &Clearance{Cookies: []Cookie{{Name: "incap_ses", Value: "abc", Domain: ".albertsons.com"}}}

	got := contextCookies("https://www.albertsons.com/shop/search-results.html?q=eggs", sc, cl)
	if len(got) != 3 {
		t.Fatalf("cookies = %d, want 3 (1 clearance + 2 store)", len(got))
	}
	if got[0].Name != "incap_ses" {
		t.Errorf("clearance cookie should come first, got %q", got[0].Name)
	}
	// Store cookies are sorted, so a failing render is reproducible.
	if got[1].Name != "STORE" || got[2].Name != "ZIP" {
		t.Errorf("store cookies out of order: %q, %q", got[1].Name, got[2].Name)
	}
	// www. is dropped so the cookie also covers the site's subdomains.
	if got[1].Domain != ".albertsons.com" {
		t.Errorf("domain = %q, want .albertsons.com", got[1].Domain)
	}
	if got[1].Path != "/" {
		t.Errorf("path = %q, want /", got[1].Path)
	}
}

func TestCookieDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.albertsons.com/shop": ".albertsons.com",
		"https://shop.example.com/x":      "shop.example.com",
		"not a url":                       "",
	}
	for in, want := range cases {
		if got := CookieDomain(in); got != want {
			t.Errorf("CookieDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
