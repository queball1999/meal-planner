// Package freefoodphotos scrapes and matches freefoodphotos.com's image
// library: given a grocery item's name, find the best-fitting CC BY 3.0
// photo caption from the site's category pages. Shared by cmd/build_catalog
// (a one-off CLI that seeds catalog/seed_items.json) and the runtime
// background image backfill (web.RunImageBackfillScheduler), which differ
// only in how a page gets fetched - see PageFetcher.
package freefoodphotos

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"goeat/pricing"
)

// Base is the root of freefoodphotos.com's image library.
const Base = "https://www.freefoodphotos.com/imagelibrary/"

// Categories maps a freefoodphotos.com category slug to our display category.
var Categories = map[string]string{
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

// Photo is one freefoodphotos.com library entry.
type Photo struct {
	Name        string
	Category    string
	ImageURL    string
	Attribution string
	words       map[string]bool // significant words of Name, normalized
}

// PageFetcher fetches one freefoodphotos.com page's HTML. cmd/build_catalog
// passes DefaultFetcher (a bare HTTP GET, fine for a one-off run); the
// runtime backfill scheduler wraps scrape.FetchSmart instead, so a page that
// comes back as a bot-wall challenge escalates to FlareSolverr/Browserless
// rather than just failing - the whole reason this is a function type and
// not a hardcoded http.Client call.
type PageFetcher func(ctx context.Context, url string) (string, error)

// DefaultFetcher is a plain HTTP GET with a descriptive User-Agent, matching
// cmd/build_catalog's original one-off behavior.
func DefaultFetcher(client *http.Client) PageFetcher {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return func(ctx context.Context, url string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
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
}

// Options configures BuildIndex.
type Options struct {
	// Slugs restricts which Categories to scrape; nil/empty means every one.
	Slugs []string
	// LimitPerCategory caps photos kept per category; 0 means no cap.
	LimitPerCategory int
	// Delay pauses between category fetches - a one-off CLI run and an
	// occasional background refresh both want to avoid firing every category
	// page back to back.
	Delay time.Duration
	// OnCategory, if set, is called after each category is scraped (slug,
	// photo count) - cmd/build_catalog uses this for its progress output.
	OnCategory func(slug string, count int)
}

// Index is a scraped, matchable snapshot of the image library. Safe for
// concurrent use: Match tracks which photos have already been assigned so
// two different items are never handed the same one, whether called from a
// one-off CLI pass or repeated background scheduler ticks.
type Index struct {
	mu     sync.Mutex
	photos []Photo
	used   map[string]bool // ImageURL already matched to some item
}

// BuildIndex scrapes every requested category page through fetch and returns
// a matchable Index.
func BuildIndex(ctx context.Context, fetch PageFetcher, opts Options) (*Index, error) {
	slugs := opts.Slugs
	if len(slugs) == 0 {
		for s := range Categories {
			slugs = append(slugs, s)
		}
	}
	sort.Strings(slugs)

	idx := &Index{used: map[string]bool{}}
	for i, slug := range slugs {
		photos, err := scrapeCategory(ctx, fetch, slug)
		if err != nil {
			return nil, fmt.Errorf("freefoodphotos: scrape %s: %w", slug, err)
		}
		if opts.LimitPerCategory > 0 && len(photos) > opts.LimitPerCategory {
			photos = photos[:opts.LimitPerCategory]
		}
		if opts.OnCategory != nil {
			opts.OnCategory(slug, len(photos))
		}
		idx.photos = append(idx.photos, photos...)
		if opts.Delay > 0 && i < len(slugs)-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(opts.Delay):
			}
		}
	}
	return idx, nil
}

// Match finds the best unused photo for name, per bestPhoto's scoring rules,
// and marks it used on a hit so a later call for a different item never
// doubles up on the same photo.
func (idx *Index) Match(name string) (Photo, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	p, ok := bestPhoto(name, idx.photos, idx.used)
	if ok {
		idx.used[p.ImageURL] = true
	}
	return p, ok
}

// IsUsed reports whether imageURL has already been matched to some item -
// cmd/build_catalog's full-mirror pass (-enrich-only=false) uses this to
// find photos nothing has claimed yet.
func (idx *Index) IsUsed(imageURL string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.used[imageURL]
}

// Photos returns every scraped photo, in scrape order - for callers (the
// full-mirror pass) that need to walk them all rather than match by name.
func (idx *Index) Photos() []Photo {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	out := make([]Photo, len(idx.photos))
	copy(out, idx.photos)
	return out
}

// Len reports how many photos the index holds.
func (idx *Index) Len() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return len(idx.photos)
}

func scrapeCategory(ctx context.Context, fetch PageFetcher, slug string) ([]Photo, error) {
	body, err := fetch(ctx, Base+slug+"/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Photo
	for _, m := range entryRE.FindAllStringSubmatch(body, -1) {
		file, caption := m[1], cleanCaption(m[2])
		if file == "" || caption == "" || seen[file] || looksComposed(caption) {
			continue
		}
		seen[file] = true
		out = append(out, Photo{
			Name:     caption,
			Category: Categories[slug],
			ImageURL: Base + slug + "/slides/" + file + ".jpg",
			Attribution: caption + " by freefoodphotos.com is licensed under a " +
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
// words with the item name. It requires every word of a short (1-2 word)
// name to be present, or at least two shared words for longer names, so
// "Salmon fillet" matches a salmon photo but "Yellow onion" never grabs
// "Deep fried onion rings".
func bestPhoto(name string, photos []Photo, used map[string]bool) (Photo, bool) {
	want := sigWords(name)
	if len(want) == 0 {
		return Photo{}, false
	}
	need := 2
	if len(want) < 2 {
		need = len(want)
	}
	var best Photo
	bestScore := 0
	for _, p := range photos {
		if used[p.ImageURL] {
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
	s = wsRE.ReplaceAllString(strings.TrimSpace(unescapeHTML(s)), " ")
	if len(s) > 70 {
		s = strings.TrimSpace(s[:70])
	}
	return s
}

func unescapeHTML(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&#39;", "'", "&quot;", `"`, "&nbsp;", " ")
	return r.Replace(s)
}
