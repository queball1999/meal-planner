package web

import "fmt"

// KnownStore is an entry in the curated store catalog shown during setup
// and on the /stores page.
type KnownStore struct {
	Name      string
	Domain    string // website domain — used for favicon lookup
	Kind      string // "grocery" | "warehouse" | "specialty" | "online"
	HasAPI    bool   // Kroger official API (§6.2)
	CanScrape bool   // CSS/XPath scraper can be configured (§6.7)
}

// KnownStores is the curated list of popular US grocery stores shown in
// the setup wizard. Ordered roughly by US market share.
var KnownStores = []KnownStore{
	{Name: "Kroger", Domain: "kroger.com", Kind: "grocery", HasAPI: true, CanScrape: true},
	{Name: "Walmart", Domain: "walmart.com", Kind: "grocery", CanScrape: true},
	{Name: "Target", Domain: "target.com", Kind: "grocery", CanScrape: true},
	{Name: "Costco", Domain: "costco.com", Kind: "warehouse", CanScrape: true},
	{Name: "Whole Foods", Domain: "wholefoodsmarket.com", Kind: "grocery", CanScrape: true},
	{Name: "Trader Joe's", Domain: "traderjoes.com", Kind: "specialty"},
	{Name: "Safeway", Domain: "safeway.com", Kind: "grocery", CanScrape: true},
	{Name: "Albertsons", Domain: "albertsons.com", Kind: "grocery", CanScrape: true},
	{Name: "Publix", Domain: "publix.com", Kind: "grocery", CanScrape: true},
	{Name: "H-E-B", Domain: "heb.com", Kind: "grocery", CanScrape: true},
	{Name: "Aldi", Domain: "aldi.us", Kind: "grocery"},
	{Name: "Sprouts", Domain: "sprouts.com", Kind: "specialty", CanScrape: true},
	{Name: "Sam's Club", Domain: "samsclub.com", Kind: "warehouse", CanScrape: true},
	{Name: "Meijer", Domain: "meijer.com", Kind: "grocery", CanScrape: true},
	{Name: "Wegmans", Domain: "wegmans.com", Kind: "grocery", CanScrape: true},
	{Name: "Food Lion", Domain: "foodlion.com", Kind: "grocery", CanScrape: true},
	{Name: "Giant Food", Domain: "giantfood.com", Kind: "grocery", CanScrape: true},
	{Name: "Instacart", Domain: "instacart.com", Kind: "online"},
}

// storeDomainMap maps lowercased store names to their web domain for fast lookup.
var storeDomainMap = func() map[string]string {
	m := make(map[string]string, len(KnownStores))
	for _, ks := range KnownStores {
		m[ks.Name] = ks.Domain
	}
	return m
}()

// StoreLogoURL returns the Google Favicon service URL for a store's logo given
// its name. Returns "" when the store name is not in the catalog.
func StoreLogoURL(name string) string {
	domain, ok := storeDomainMap[name]
	if !ok || domain == "" {
		return ""
	}
	return fmt.Sprintf("https://www.google.com/s2/favicons?domain=%s&sz=64", domain)
}

// KnownStoreByName returns the catalog entry for name, or nil.
func KnownStoreByName(name string) *KnownStore {
	for i := range KnownStores {
		if KnownStores[i].Name == name {
			return &KnownStores[i]
		}
	}
	return nil
}
