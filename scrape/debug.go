package scrape

import (
	"net/url"
	"sync"
	"time"
)

// DebugEntry records one FetchSmart call so the admin Audit Log can show why a
// page came back empty, alongside the LLM call log.
type DebugEntry struct {
	At         time.Time
	URL        string
	Host       string
	Backend    string // renderer that served it ("Browserless", …); "" = direct fetch
	ViaProxy   bool   // served by a headless browser rather than a direct request
	StatusCode int
	Bytes      int    // length of the returned HTML
	Challenge  string // non-empty when the page looked like a bot wall
	Error      string
	DurationMS int64
}

const maxScrapeDebugEntries = 50

// scrapeDebugLog is an in-memory ring of recent scrape fetches.
type scrapeDebugLog struct {
	mu   sync.Mutex
	ring []DebugEntry
}

// GlobalDebugLog captures every FetchSmart call. Always non-nil - no wiring
// needed, unlike llm.GlobalDebugLog which wraps a generator.
var GlobalDebugLog = &scrapeDebugLog{}

// Record appends one entry, dropping the oldest past the cap.
func (d *scrapeDebugLog) Record(e DebugEntry) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Host == "" {
		e.Host = hostOf(e.URL)
	}
	d.mu.Lock()
	d.ring = append(d.ring, e)
	if len(d.ring) > maxScrapeDebugEntries {
		d.ring = d.ring[len(d.ring)-maxScrapeDebugEntries:]
	}
	d.mu.Unlock()
}

// Entries returns a snapshot of the ring, newest last.
func (d *scrapeDebugLog) Entries() []DebugEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DebugEntry, len(d.ring))
	copy(out, d.ring)
	return out
}

func hostOf(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil {
		return parsed.Host
	}
	return ""
}
