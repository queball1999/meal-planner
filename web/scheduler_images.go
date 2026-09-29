package web

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"goeat/db"
	"goeat/freefoodphotos"
	"goeat/scrape"
)

// imageBackfillInterval paces the background photo backfill - see
// RunImageBackfillScheduler's doc comment for why one item per tick is the
// right amount of "slowly, over time."
const imageBackfillInterval = 5 * time.Minute

// freefoodphotosIndexTTL is how long the scraped-and-matched photo index is
// trusted before a tick rebuilds it. The library itself rarely changes; this
// just bounds how stale a mismatch (a caption the site has since edited or
// removed) can go unnoticed, not a real freshness need.
const freefoodphotosIndexTTL = 24 * time.Hour

// ffpIndex is the shared, lazily (re)built freefoodphotos photo index. It is
// package-level rather than per-Server because building it makes one request
// per category through the scraping pipeline - deliberately rare and only
// ever triggered by RunImageBackfillScheduler, never by a page view (see
// warmFreefoodphotosIndexIfBuilt, which lazyFetchItemImage uses instead).
var (
	ffpIndexMu      sync.Mutex
	ffpIndex        *freefoodphotos.Index
	ffpIndexBuiltAt time.Time
)

// lastImageBackfillCheck reports when RunImageBackfillScheduler last ticked
// (zero value if it was never started, or hasn't ticked yet) - matches
// lastAutoPlanCheck's role for the About page's background-process list.
func (s *Server) lastImageBackfillCheck() time.Time {
	s.imageBackfillMu.Lock()
	defer s.imageBackfillMu.Unlock()
	return s.imageBackfillCheckedAt
}

// RunImageBackfillScheduler slowly discovers and downloads photos for
// catalog items that have none, so an item nobody happens to view still
// eventually gets one - lazyFetchItemImage (web/handlers_items.go) and its
// pantry-list caller only ever fire when a page is actually viewed.
//
// One item per tick, ticking every imageBackfillInterval: combined with the
// existing per-download gate/2s spacing and 15-minute failure backoff
// (itemImageGate et al. in handlers_items.go, reused as-is here), this is
// the "slowly, over time" pacing - it never competes with, or duplicates,
// the reactive path's own throttling, and it never bursts requests at
// freefoodphotos.com.
func (s *Server) RunImageBackfillScheduler(ctx context.Context) {
	if s.itemImageDir == "" {
		return // nothing to save a download to
	}
	t := time.NewTicker(imageBackfillInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.imageBackfillTick(ctx)
		}
	}
}

func (s *Server) imageBackfillTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("image backfill: recovered from panic: %v", r)
		}
	}()

	s.imageBackfillMu.Lock()
	s.imageBackfillCheckedAt = time.Now()
	s.imageBackfillMu.Unlock()

	// Photos are matched by item name, so which household an item belongs to
	// doesn't matter here - walk every household's catalog in turn, still
	// downloading at most one image per tick across all of them.
	households, err := s.store.ListHouseholds(ctx)
	if err != nil {
		log.Printf("image backfill: list households: %v", err)
		return
	}
	var items []*db.Item
	for _, hh := range households {
		hhItems, err := s.store.ListItems(ctx, hh.ID)
		if err != nil {
			log.Printf("image backfill: list items for household %d: %v", hh.ID, err)
			continue
		}
		items = append(items, hhItems...)
	}

	render := s.renderConfig(ctx)

	// Walk items (in the stable, alphabetical ListItems order) looking for one
	// ready to download: either already matched, or matchable right now. An
	// item that matches nothing is skipped rather than stopping the scan here
	// - items are listed by name, so an early, permanently-unmatchable item
	// (e.g. "2% milk") would otherwise block every later item forever.
	var (
		target *db.Item
		idx    *freefoodphotos.Index
	)
	for _, it := range items {
		if it.ImagePath != "" {
			continue
		}
		if it.ImageSourceURL != "" {
			target = it
			break
		}
		if idx == nil {
			var ierr error
			idx, ierr = s.warmFreefoodphotosIndex(ctx, render)
			if ierr != nil {
				log.Printf("image backfill: build index: %v", ierr)
				return
			}
		}
		p, ok := idx.Match(it.Name)
		if !ok {
			// Nothing in the library matches this item's name. Leave it
			// alone rather than retrying every tick forever - a curated
			// re-seed or a manual upload is what would actually fix this one.
			continue
		}
		if err := s.store.SetItemImageSource(ctx, it.ID, p.ImageURL, p.Attribution); err != nil {
			log.Printf("image backfill: save source for item %d: %v", it.ID, err)
			continue
		}
		log.Printf("image backfill: matched %d (%s) -> %s", it.ID, it.Name, p.ImageURL)
		it.ImageSourceURL = p.ImageURL
		it.ImageAttribution = p.Attribution
		target = it
		break
	}
	if target == nil {
		return // every item already has a photo, or none of the rest match anything
	}

	s.fetchAndStoreItemImage(target, render)
}

// warmFreefoodphotosIndex returns the cached photo index, (re)building it
// through the scraping pipeline when missing or older than
// freefoodphotosIndexTTL. Rebuilding is the only part of this feature that
// makes more than one request at a time (one per freefoodphotos.com
// category), so it only ever runs from here - the backfill scheduler - never
// from a page view.
func (s *Server) warmFreefoodphotosIndex(ctx context.Context, render scrape.RenderConfig) (*freefoodphotos.Index, error) {
	ffpIndexMu.Lock()
	defer ffpIndexMu.Unlock()
	if ffpIndex != nil && time.Since(ffpIndexBuiltAt) < freefoodphotosIndexTTL {
		return ffpIndex, nil
	}

	// A direct fetch is tried first (scrape.FetchSmart's own behavior) and
	// only escalates to FlareSolverr/Browserless when the page actually comes
	// back as a bot-wall challenge - the same posture the store-price
	// scraper already takes, just applied to freefoodphotos.com's category
	// pages instead of a grocery site's search results.
	fetch := freefoodphotos.PageFetcher(func(fctx context.Context, url string) (string, error) {
		smart, err := scrape.FetchSmart(fctx, url, render)
		if err != nil {
			return "", err
		}
		if smart.Challenge != "" {
			return "", fmt.Errorf("blocked: %s", smart.Challenge)
		}
		return smart.HTML, nil
	})

	idx, err := freefoodphotos.BuildIndex(ctx, fetch, freefoodphotos.Options{
		Delay: time.Second, // space category-page requests out even on a rebuild
	})
	if err != nil {
		return nil, err
	}
	ffpIndex, ffpIndexBuiltAt = idx, time.Now()
	return ffpIndex, nil
}

// warmFreefoodphotosIndexIfBuilt returns the cached index without ever
// triggering a rebuild - the reactive path (a page view, via
// ensureItemImageSource) consults whatever the backfill scheduler has
// already warmed and never scrapes freefoodphotos.com on its own. Returns
// nil before the scheduler's first successful tick.
func warmFreefoodphotosIndexIfBuilt() *freefoodphotos.Index {
	ffpIndexMu.Lock()
	defer ffpIndexMu.Unlock()
	return ffpIndex
}
