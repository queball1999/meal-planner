// Command build_catalog scrapes freefoodphotos.com's image library and emits a
// seed_items.json for package catalog: one builtin item per photo, each carrying
// image_source_url (the full-size JPG) and image_attribution (the CC BY 3.0
// credit line the site requires). The runtime image pipeline in package items
// then lazily downloads and caches each photo on first view.
//
// It MERGES onto the existing hand-curated catalog/seed_items.json: curated
// entries keep their stock_unit / default_purchase_qty / conversions and just
// gain the image fields when a photo caption matches; photos with no curated
// match are appended as new "each" items.
//
//	go run ./cmd/build_catalog                 # merge, write seed_items.generated.json
//	go run ./cmd/build_catalog -limit 5        # 5 photos per category (smoke test)
//	go run ./cmd/build_catalog -categories fruit,vegetables
//	go run ./cmd/build_catalog -out catalog/seed_items.json   # overwrite in place
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"goeat/pricing"
)

const base = "https://www.freefoodphotos.com/imagelibrary/"

// category slug on freefoodphotos.com -> our display category.
var categories = map[string]string{
	"vegetables":    "Produce",
	"fruit":         "Produce",
	"herbs":         "Produce",
	"meat":          "Meat & Seafood",
	"seafood":       "Meat & Seafood",
	"dairy":         "Dairy & Eggs",
	"bread":         "Bakery",
	"confectionery": "Snacks & Sweets",
	"cooking":       "Pantry",
	"seasonal":      "Pantry",
}

// <a href="slides/NAME.html" ...><img ... src="thumbs/NAME.jpg" ... alt="CAPTION" ...>
var entryRE = regexp.MustCompile(
	`href="slides/([a-z0-9_\-]+)\.html"[^>]*>\s*<img[^>]*alt="([^"]*)"`)

var wsRE = regexp.MustCompile(`\s+`)

type seedConversion struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Factor float64 `json:"factor"`
}

type seedItem struct {
	Name               string           `json:"name"`
	Category           string           `json:"category"`
	StockUnit          string           `json:"stock_unit"`
	DefaultPurchaseQty float64          `json:"default_purchase_qty"`
	ImageSourceURL     string           `json:"image_source_url,omitempty"`
	ImageAttribution   string           `json:"image_attribution,omitempty"`
	Conversions        []seedConversion `json:"conversions,omitempty"`
}

type seedFile struct {
	Comment string     `json:"_comment,omitempty"`
	Items   []seedItem `json:"items"`
}

func main() {
	out := flag.String("out", "catalog/seed_items.generated.json", "output path")
	mergePath := flag.String("merge", "catalog/seed_items.json", "curated file to merge onto ('' to skip)")
	limit := flag.Int("limit", 0, "max photos per category (0 = all)")
	catList := flag.String("categories", "", "comma-separated subset of freefoodphotos categories")
	enrichOnly := flag.Bool("enrich-only", false, "only attach photos to curated items; never append photo captions as new items")
	flag.Parse()

	want := map[string]bool{}
	for _, c := range strings.Split(*catList, ",") {
		if c = strings.TrimSpace(c); c != "" {
			want[c] = true
		}
	}

	client := &http.Client{Timeout: 25 * time.Second}

	// Curated entries first, indexed by normalized name so scraped photos can
	// attach their image fields to the right row.
	var items []seedItem
	byTerm := map[string]int{} // normalized term -> index in items
	if *mergePath != "" {
		cur, err := loadCurated(*mergePath)
		if err != nil {
			fatal("read %s: %v", *mergePath, err)
		}
		items = cur
		for i, it := range items {
			byTerm[pricing.Normalize(it.Name)] = i
		}
		fmt.Fprintf(os.Stderr, "curated: %d items\n", len(items))
	}

	slugs := make([]string, 0, len(categories))
	for s := range categories {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)

	var all []photo
	for _, slug := range slugs {
		if len(want) > 0 && !want[slug] {
			continue
		}
		photos, err := scrapeCategory(client, slug)
		if err != nil {
			fatal("scrape %s: %v", slug, err)
		}
		if *limit > 0 && len(photos) > *limit {
			photos = photos[:*limit]
		}
		fmt.Fprintf(os.Stderr, "%-14s %d photos\n", slug, len(photos))
		all = append(all, photos...)
		time.Sleep(400 * time.Millisecond)
	}

	// Pass 1: attach a photo to every curated item that lacks one, matching on
	// shared significant words between the curated name and the photo caption.
	used := map[string]bool{}
	var enriched int
	for i := range items {
		if items[i].ImageSourceURL != "" {
			continue
		}
		if p, ok := bestPhoto(items[i].Name, all, used); ok {
			items[i].ImageSourceURL = p.imageURL
			items[i].ImageAttribution = p.attribution
			used[p.imageURL] = true
			enriched++
		}
	}

	// Pass 2 (full mirror): append every remaining photo as its own builtin item.
	var added int
	if !*enrichOnly {
		for _, p := range all {
			if used[p.imageURL] {
				continue
			}
			term := pricing.Normalize(p.name)
			if term == "" {
				continue
			}
			if _, ok := byTerm[term]; ok {
				continue
			}
			byTerm[term] = len(items)
			items = append(items, seedItem{
				Name:               p.name,
				Category:           p.category,
				StockUnit:          "each",
				DefaultPurchaseQty: 1,
				ImageSourceURL:     p.imageURL,
				ImageAttribution:   p.attribution,
			})
			added++
		}
	}

	sf := seedFile{
		Comment: "Starter grocery catalog seeded per household on first boot (source='builtin'). " +
			"Image rows scraped from freefoodphotos.com (CC BY 3.0) by cmd/build_catalog; " +
			"the runtime image pipeline downloads and caches each photo lazily on first view.",
		Items: items,
	}
	buf, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		fatal("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %d items (%d new, %d enriched)\n", *out, len(items), added, enriched)
}

type photo struct {
	name, category, imageURL, attribution string
	words                                 map[string]bool // significant words of name, normalized
}

func scrapeCategory(client *http.Client, slug string) ([]photo, error) {
	body, err := get(client, base+slug+"/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []photo
	for _, m := range entryRE.FindAllStringSubmatch(body, -1) {
		file, caption := m[1], cleanCaption(m[2])
		if file == "" || caption == "" || seen[file] || looksComposed(caption) {
			continue
		}
		seen[file] = true
		out = append(out, photo{
			name:     caption,
			category: categories[slug],
			imageURL: base + slug + "/slides/" + file + ".jpg",
			attribution: caption + " by freefoodphotos.com is licensed under a " +
				"Creative Commons Attribution 3.0 Unported License",
			words: sigWords(caption),
		})
	}
	return out, nil
}

// stop words that carry no grocery-identity signal in a photo caption.
var stop = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "on": true, "in": true, "with": true,
	"and": true, "for": true, "to": true, "fresh": true, "raw": true, "whole": true,
	"isolated": true, "background": true, "closeup": true, "close": true, "up": true,
	"white": true, "wooden": true, "board": true, "bowl": true, "plate": true, "table": true,
	"photo": true, "image": true, "delicious": true, "tasty": true, "some": true,
}

func sigWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(pricing.Normalize(s)) {
		if !stop[w] && len(w) > 1 {
			out[w] = true
		}
	}
	return out
}

// bestPhoto picks the unused photo whose caption shares the most significant
// words with the curated item name. It requires every word of a short (1-2
// word) name to be present, or at least two shared words for longer names, so
// "Salmon fillet" matches a salmon photo but "Yellow onion" never grabs
// "Deep fried onion rings".
func bestPhoto(name string, photos []photo, used map[string]bool) (photo, bool) {
	want := sigWords(name)
	if len(want) == 0 {
		return photo{}, false
	}
	need := 2
	if len(want) < 2 {
		need = len(want)
	}
	var best photo
	bestScore := 0
	for _, p := range photos {
		if used[p.imageURL] {
			continue
		}
		score := 0
		for w := range want {
			if p.words[w] {
				score++
			}
		}
		if score < need || score <= bestScore {
			continue
		}
		// prefer a tight caption: fewer extra words = more likely the subject.
		if score == bestScore && len(p.words) >= len(best.words) {
			continue
		}
		best, bestScore = p, score
	}
	return best, bestScore >= need
}

// composedRE flags captions that describe a scene, a prepared dish, or several
// items at once rather than a single grocery item ("Beef patties and sausages
// on a barbecue", "Assorted meat grilling over a BBQ fire").
var composedRE = regexp.MustCompile(`(?i)\b(` +
	`and|with|&|` +
	`barbecue|barbeque|bbq|grill|grilling|grilled|` +
	`cook|cooking|cooked|roast|roasting|roasted|frying|fried|baking|baked|` +
	`saute|sauteed|steamed|boiled|poached|toasted|smoked|marinated|stuffed|` +
	`platter|plate|plated|bowl|dish|meal|dinner|lunch|breakfast|brunch|buffet|` +
	`spread|selection|assorted|assortment|various|mixed|collection|` +
	`ingredients|recipe|served|serving|garnished|topped|drizzled|sprinkled|` +
	`ready|preparing|preparation|homemade|leftover|` +
	`pile|piled|heap|heaped|mound|mounded|scattered|strewn|handful|handfuls|` +
	`bunch|bunches|bundle|bundles|cluster|clusters|stack|stacked|` +
	`arrangement|arranged|display|displayed|market|stall|basket|crate|sack|` +
	`salad|soup|stew|curry|casserole|sandwich|burger|pizza|sauce|` +
	`over|on a|in a` +
	`)\b`)

func looksComposed(caption string) bool {
	if composedRE.MatchString(caption) {
		return true
	}
	return len(sigWords(caption)) > 4
}

func cleanCaption(s string) string {
	s = wsRE.ReplaceAllString(strings.TrimSpace(html(s)), " ")
	if len(s) > 70 {
		s = strings.TrimSpace(s[:70])
	}
	return s
}

func html(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&#39;", "'", "&quot;", `"`, "&nbsp;", " ")
	return r.Replace(s)
}

func loadCurated(path string) ([]seedItem, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf seedFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return nil, err
	}
	return sf.Items, nil
}

func get(client *http.Client, url string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "goeat-build_catalog/1.0 (+https://www.freefoodphotos.com)")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return string(b), err
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "build_catalog: "+format+"\n", a...)
	os.Exit(1)
}
