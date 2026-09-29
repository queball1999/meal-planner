package web

import (
	"strconv"
	"strings"
)

// KnownStore is an entry in the curated store catalog shown during setup
// and on the /stores page.
type KnownStore struct {
	Name      string
	Domain    string // website domain - used for favicon lookup
	Kind      string // "grocery" | "warehouse" | "specialty" | "online"
	HasAPI    bool   // Kroger official API (§6.2)
	CanScrape bool   // CSS/XPath scraper can be configured (§6.7)

	// SearchURL is the store's product-search URL with {term} where the
	// ingredient goes. Prefilled into a new scrape config so a store starts
	// out pointing at the right page even when we cannot scrape it yet - the
	// operator only has to pick selectors, not hunt for the URL.
	SearchURL string

	// States lists the two-letter states the chain operates in. Empty means
	// nationwide. Used to grey out stores that do not exist near a household's
	// ZIP code (there is no Publix in Arizona).
	States []string
}

// KnownStores is the curated list of popular US grocery stores shown in
// the setup wizard. Ordered roughly by US market share.
var KnownStores = []KnownStore{
	{
		Name: "Kroger", Domain: "kroger.com", Kind: "grocery", HasAPI: true, CanScrape: true,
		SearchURL: "https://www.kroger.com/search?query={term}",
		// Arizona is served by Fry's, Kroger's banner there - not by
		// Kroger-branded stores, so AZ is deliberately absent here.
		States: []string{"AL", "AR", "AK", "CO", "GA", "IL", "IN", "KY", "LA", "MI", "MS",
			"NV", "OH", "OR", "SC", "TN", "TX", "UT", "VA", "WA", "WV", "WI"},
	},
	{
		// Kroger's Arizona banner and the state's second-largest chain. Covered
		// by the same Kroger developer API as Kroger-branded stores.
		Name: "Fry's Food and Drug", Domain: "frysfood.com", Kind: "grocery", HasAPI: true, CanScrape: true,
		SearchURL: "https://www.frysfood.com/search?query={term}",
		States:    []string{"AZ"},
	},
	{
		// Arizona family chain; also operates the Food City and AJ's banners.
		Name: "Bashas'", Domain: "bashas.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.bashas.com/search/?q={term}",
		States:    []string{"AZ", "NM"},
	},
	{
		Name: "Food City", Domain: "foodcityaz.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.foodcityaz.com/search/?q={term}",
		States:    []string{"AZ"},
	},
	{
		Name: "IGA", Domain: "iga.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.iga.com/search?q={term}",
	},
	{
		Name: "Walmart", Domain: "walmart.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.walmart.com/search?q={term}",
	},
	{
		Name: "Target", Domain: "target.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.target.com/s?searchTerm={term}",
	},
	{
		Name: "Costco", Domain: "costco.com", Kind: "warehouse", CanScrape: true,
		SearchURL: "https://www.costco.com/CatalogSearch?keyword={term}",
	},
	{
		Name: "Whole Foods", Domain: "wholefoodsmarket.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.wholefoodsmarket.com/search?text={term}",
	},
	{
		Name: "Trader Joe's", Domain: "traderjoes.com", Kind: "specialty",
		SearchURL: "https://www.traderjoes.com/home/search?q={term}",
	},
	{
		Name: "Safeway", Domain: "safeway.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.safeway.com/shop/search-results.html?q={term}",
		States: []string{"AK", "AZ", "CA", "CO", "DC", "DE", "HI", "ID", "MD", "MT", "NE", "NV",
			"NM", "OR", "SD", "VA", "WA", "WY"},
	},
	{
		Name: "Albertsons", Domain: "albertsons.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.albertsons.com/shop/search-results.html?q={term}",
		States: []string{"AK", "AL", "AR", "AZ", "CA", "CO", "DE", "FL", "ID", "LA", "ME", "MD",
			"MT", "NE", "NV", "NH", "NJ", "NM", "NY", "ND", "OR", "PA", "RI", "SD", "TX", "UT",
			"VT", "VA", "WA", "WY"},
	},
	{
		Name: "Publix", Domain: "publix.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.publix.com/search?query={term}",
		States:    []string{"AL", "FL", "GA", "KY", "NC", "SC", "TN", "VA"},
	},
	{
		Name: "H-E-B", Domain: "heb.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.heb.com/search?q={term}",
		States:    []string{"TX"},
	},
	{
		Name: "Aldi", Domain: "aldi.us", Kind: "grocery",
		SearchURL: "https://www.aldi.us/results?q={term}",
		States: []string{"AL", "AR", "AZ", "CA", "CT", "DC", "DE", "FL", "GA", "IA", "IL", "IN",
			"KS", "KY", "LA", "MA", "MD", "ME", "MI", "MN", "MO", "MS", "NC", "ND", "NE", "NH",
			"NJ", "NY", "OH", "OK", "PA", "RI", "SC", "SD", "TN", "TX", "VA", "VT", "WI", "WV"},
	},
	{
		Name: "Sprouts", Domain: "sprouts.com", Kind: "specialty", CanScrape: true,
		SearchURL: "https://shop.sprouts.com/search?search_term={term}",
		States: []string{"AL", "AZ", "CA", "CO", "DE", "FL", "GA", "KS", "LA", "MD", "MO", "NC",
			"NJ", "NM", "NV", "OK", "PA", "SC", "TN", "TX", "UT", "VA", "WA"},
	},
	{
		Name: "Sam's Club", Domain: "samsclub.com", Kind: "warehouse", CanScrape: true,
		SearchURL: "https://www.samsclub.com/s/{term}",
	},
	{
		Name: "Meijer", Domain: "meijer.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.meijer.com/shopping/search.html?text={term}",
		States:    []string{"IL", "IN", "KY", "MI", "OH", "WI"},
	},
	{
		Name: "Wegmans", Domain: "wegmans.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://shop.wegmans.com/search?search_term={term}",
		States:    []string{"DC", "DE", "MA", "MD", "NC", "NJ", "NY", "PA", "VA"},
	},
	{
		Name: "Food Lion", Domain: "foodlion.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://www.foodlion.com/search?q={term}",
		States:    []string{"DE", "GA", "KY", "MD", "NC", "PA", "SC", "TN", "VA", "WV"},
	},
	{
		Name: "Giant Food", Domain: "giantfood.com", Kind: "grocery", CanScrape: true,
		SearchURL: "https://giantfood.com/search?q={term}",
		States:    []string{"DC", "DE", "MD", "VA"},
	},
	{
		Name: "Instacart", Domain: "instacart.com", Kind: "online",
		SearchURL: "https://www.instacart.com/store/s?k={term}",
	},
}

// storeDomainMap maps lowercased store names to their web domain for fast lookup.
var storeDomainMap = func() map[string]string {
	m := make(map[string]string, len(KnownStores))
	for _, ks := range KnownStores {
		m[ks.Name] = ks.Domain
	}
	return m
}()

// StoreLogoURL returns the same-origin URL of a store's cached logo given its
// name (see store_logos.go). Returns "" when the store is not in the catalog.
func StoreLogoURL(name string) string {
	domain, ok := storeDomainMap[name]
	if !ok || domain == "" {
		return ""
	}
	return "/store-logos/" + domain
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

// AvailableIn reports whether the chain operates in a two-letter state code.
// A store with no state list is nationwide; an unknown state is treated as
// available so a bad ZIP never hides every option.
func (k KnownStore) AvailableIn(state string) bool {
	if len(k.States) == 0 || state == "" {
		return true
	}
	for _, s := range k.States {
		if s == state {
			return true
		}
	}
	return false
}

// StatesCSV renders the chain's footprint for a tooltip ("" = nationwide).
func (k KnownStore) StatesCSV() string { return strings.Join(k.States, ",") }

// ── ZIP → state ───────────────────────────────────────────────────────────────

// zipRange is one contiguous block of ZIP codes belonging to a state. The USPS
// allocates ZIPs geographically, so a state's coverage is a handful of ranges -
// far smaller than a full ZIP-to-place table and enough to answer "is there a
// Publix near me".
type zipRange struct {
	lo, hi int
	state  string
}

var zipRanges = []zipRange{
	{1000, 2799, "MA"}, {2800, 2999, "RI"}, {3000, 3899, "NH"}, {3900, 4999, "ME"},
	{5000, 5999, "VT"}, {6000, 6999, "CT"}, {7000, 8999, "NJ"},
	{10000, 14999, "NY"}, {15000, 19699, "PA"}, {19700, 19999, "DE"},
	{20000, 20599, "DC"}, {20600, 21999, "MD"}, {22000, 24699, "VA"}, {24700, 26899, "WV"},
	{27000, 28999, "NC"}, {29000, 29999, "SC"}, {30000, 31999, "GA"}, {32000, 34999, "FL"},
	{35000, 36999, "AL"}, {37000, 38599, "TN"}, {38600, 39799, "MS"},
	{40000, 42799, "KY"}, {43000, 45999, "OH"}, {46000, 47999, "IN"}, {48000, 49999, "MI"},
	{50000, 52899, "IA"}, {53000, 54999, "WI"}, {55000, 56799, "MN"},
	{57000, 57799, "SD"}, {58000, 58899, "ND"}, {59000, 59999, "MT"},
	{60000, 62999, "IL"}, {63000, 65899, "MO"}, {66000, 67999, "KS"}, {68000, 69399, "NE"},
	{70000, 71499, "LA"}, {71600, 72999, "AR"}, {73000, 74999, "OK"},
	{75000, 79999, "TX"}, {88500, 88599, "TX"},
	{80000, 81699, "CO"}, {82000, 83199, "WY"}, {83200, 83899, "ID"},
	{84000, 84799, "UT"}, {85000, 86599, "AZ"}, {87000, 88499, "NM"},
	{88900, 89899, "NV"}, {90000, 96199, "CA"}, {96700, 96899, "HI"},
	{97000, 97999, "OR"}, {98000, 99499, "WA"}, {99500, 99999, "AK"},
}

// StateForZIP maps a 5-digit US ZIP code to a two-letter state code. Returns ""
// for anything it cannot place, which callers treat as "show everything".
func StateForZIP(zip string) string {
	zip = strings.TrimSpace(zip)
	if len(zip) > 5 {
		zip = zip[:5]
	}
	if len(zip) != 5 {
		return ""
	}
	n, err := strconv.Atoi(zip)
	if err != nil {
		return ""
	}
	for _, r := range zipRanges {
		if n >= r.lo && n <= r.hi {
			return r.state
		}
	}
	return ""
}
