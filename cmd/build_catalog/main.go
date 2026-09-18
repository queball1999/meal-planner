// Command build_catalog scrapes freefoodphotos.com's image library to attach
// photos to the hand-curated catalog/seed_items.json: a curated entry keeps its
// stock_unit / default_purchase_qty / conversions and just gains
// image_source_url (the full-size JPG) and image_attribution (the CC BY 3.0
// credit line the site requires) when a photo caption matches. The runtime
// image pipeline in package items then lazily downloads and caches each photo
// on first view.
//
// The scrape/match logic itself lives in package freefoodphotos, shared with
// the runtime background image backfill (web.RunImageBackfillScheduler).
//
// Photo captions are vague scene descriptions ("Bone-in thick juicy raw ribeye
// beef steak"), so by default (-enrich-only) unmatched photos are dropped. Pass
// -enrich-only=false to also append every unmatched caption as its own "each"
// item, producing a full photo mirror.
//
//	go run ./cmd/build_catalog                 # merge, write seed_items.generated.json
//	go run ./cmd/build_catalog -limit 5        # 5 photos per category (smoke test)
//	go run ./cmd/build_catalog -categories fruit,vegetables
//	go run ./cmd/build_catalog -out catalog/seed_items.json   # overwrite in place
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"goeat/freefoodphotos"
	"goeat/pricing"
)

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
	enrichOnly := flag.Bool("enrich-only", true, "only attach photos to curated items; never append photo captions as new items (photo-caption items are vague and not wanted in the seed)")
	flag.Parse()

	var slugs []string
	for _, c := range strings.Split(*catList, ",") {
		if c = strings.TrimSpace(c); c != "" {
			slugs = append(slugs, c)
		}
	}

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

	client := &http.Client{Timeout: 25 * time.Second}
	idx, err := freefoodphotos.BuildIndex(context.Background(), freefoodphotos.DefaultFetcher(client), freefoodphotos.Options{
		Slugs:            slugs,
		LimitPerCategory: *limit,
		Delay:            400 * time.Millisecond,
		OnCategory: func(slug string, n int) {
			fmt.Fprintf(os.Stderr, "%-14s %d photos\n", slug, n)
		},
	})
	if err != nil {
		fatal("%v", err)
	}

	// Pass 1: attach a photo to every curated item that lacks one, matching on
	// shared significant words between the curated name and the photo caption.
	var enriched int
	for i := range items {
		if items[i].ImageSourceURL != "" {
			continue
		}
		if p, ok := idx.Match(items[i].Name); ok {
			items[i].ImageSourceURL = p.ImageURL
			items[i].ImageAttribution = p.Attribution
			enriched++
		}
	}

	// Pass 2 (full mirror): append every remaining photo as its own builtin item.
	var added int
	if !*enrichOnly {
		for _, p := range idx.Photos() {
			if idx.IsUsed(p.ImageURL) {
				continue
			}
			term := pricing.Normalize(p.Name)
			if term == "" {
				continue
			}
			if _, ok := byTerm[term]; ok {
				continue
			}
			byTerm[term] = len(items)
			items = append(items, seedItem{
				Name:               p.Name,
				Category:           p.Category,
				StockUnit:          "each",
				DefaultPurchaseQty: 1,
				ImageSourceURL:     p.ImageURL,
				ImageAttribution:   p.Attribution,
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

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "build_catalog: "+format+"\n", a...)
	os.Exit(1)
}
