package pricing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Phases of a background pricing run, in the order a run moves through them.
// PhaseRepair only happens after generation, and only when the priced plan
// came out over budget (plan.Repair) - each repair round then starts a fresh
// pass through the phases before it.
const (
	PhasePreparing = "preparing" // linking ingredients, seeding the list
	PhaseLookup    = "lookup"    // store prices, one line at a time
	PhaseAI        = "ai"        // one batched AI estimate for the misses
	PhaseSaving    = "saving"    // reconciling units and writing each row
	PhaseRepair    = "repair"    // swapping meals to get back under budget
	PhaseDone      = "done"
)

// Where one line is in a pass.
const (
	LineWaiting  = "waiting"  // not reached yet
	LineLooking  = "looking"  // store lookups in flight
	LineQueued   = "queued"   // no store answered; waiting on the AI estimate
	LinePriced   = "priced"   // has a price (see ProgressLine.How for where from)
	LineUnpriced = "unpriced" // nothing answered and there was no guess to fall back on
)

// How a priced line got its price - what the dialog's tally is grouped by.
const (
	HowStore = "store" // a saved, cached, live or scraped store price
	HowAI    = "ai"    // the batched AI estimate
	HowGuess = "guess" // the meal plan's own per-ingredient guess
)

// maxProgressEvents bounds a run's activity log. A run notes roughly one
// event per line plus a handful per phase, so this only truncates a list far
// longer than a week of meals produces; the oldest events go first.
const maxProgressEvents = 300

// progressRetention is how long a finished run stays readable (the dialog
// can still show how the last run went) before StartProgress prunes it.
const progressRetention = 6 * time.Hour

// ProgressLine is one shopping-list line's state in the current pass.
type ProgressLine struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	How        string `json:"how,omitempty"`
	Source     string `json:"source,omitempty"` // human label: "Cached price", "AI estimate", ...
	Store      string `json:"store,omitempty"`
	PriceCents int64  `json:"price_cents,omitempty"`
}

// ProgressEvent is one entry in a run's activity log.
type ProgressEvent struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"` // "info" | "warn"
	Message string    `json:"message"`
}

// ProgressCounts tallies a pass's lines for the dialog's summary row.
type ProgressCounts struct {
	Store    int `json:"store"`
	AI       int `json:"ai"`
	Guess    int `json:"guess"`
	Unpriced int `json:"unpriced"`
	Pending  int `json:"pending"` // waiting, looking, or queued
}

// ProgressSnapshot is a copy of a run's state, safe to hand to a handler.
type ProgressSnapshot struct {
	Kind      string          `json:"kind"`
	Phase     string          `json:"phase"`
	Status    string          `json:"status"` // one-line "what is happening right now"
	Active    bool            `json:"active"`
	Stopped   bool            `json:"stopped"`
	StartedAt time.Time       `json:"started_at"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Pass      int             `json:"pass"`
	Total     int             `json:"total"`
	Done      int             `json:"done"`
	Counts    ProgressCounts  `json:"counts"`
	Lines     []ProgressLine  `json:"lines"`
	Events    []ProgressEvent `json:"events"`
}

// Progress tracks one background pricing run for the shopping list's "Pricing
// progress" dialog. It lives in memory only: a run does not survive a
// restart, so neither does its progress.
//
// Every method is nil-safe, the same way plan.Job's Emit helpers are - the
// pricing code reports to whatever ProgressFrom(ctx) returns, and a caller
// that never attached one (tests, the mid-draft price check) costs nothing.
type Progress struct {
	mu        sync.Mutex
	kind      string
	phase     string
	startedAt time.Time
	endedAt   time.Time
	stopped   bool
	pass      int
	current   int // index of the line being looked up, -1 when none
	lines     []ProgressLine
	events    []ProgressEvent
}

var (
	progressMu  sync.Mutex
	progressByP = map[int64]*Progress{}
)

// StartProgress begins tracking a pricing run for planID, replacing whatever
// earlier run was recorded for it. kind names what started the run ("New
// plan", "Reprice", ...). Call Finish when the run ends.
func StartProgress(planID int64, kind string) *Progress {
	p := &Progress{kind: kind, phase: PhasePreparing, startedAt: time.Now(), current: -1}
	p.Note("Started: " + kind)

	progressMu.Lock()
	for id, old := range progressByP {
		if old.finishedBefore(time.Now().Add(-progressRetention)) {
			delete(progressByP, id)
		}
	}
	progressByP[planID] = p
	progressMu.Unlock()
	return p
}

// GetProgress returns the most recent run recorded for planID, finished or
// not, or nil when there has been none since the process started.
func GetProgress(planID int64) *Progress {
	progressMu.Lock()
	defer progressMu.Unlock()
	return progressByP[planID]
}

type progressKey struct{}

// WithProgress attaches p to ctx so the pricing code under it can report to
// it without every signature in between growing a parameter.
func WithProgress(ctx context.Context, p *Progress) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, p)
}

// ProgressFrom returns the run attached to ctx, or nil.
func ProgressFrom(ctx context.Context) *Progress {
	p, _ := ctx.Value(progressKey{}).(*Progress)
	return p
}

func (p *Progress) finishedBefore(t time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.endedAt.IsZero() && p.endedAt.Before(t)
}

// Note appends an entry to the activity log.
func (p *Progress) Note(message string) { p.event("info", message) }

// Warn appends an entry the dialog shows as a problem.
func (p *Progress) Warn(message string) { p.event("warn", message) }

func (p *Progress) event(level, message string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.appendEvent(level, message)
	p.mu.Unlock()
}

// appendEvent needs p.mu held.
func (p *Progress) appendEvent(level, message string) {
	p.events = append(p.events, ProgressEvent{At: time.Now(), Level: level, Message: message})
	if over := len(p.events) - maxProgressEvents; over > 0 {
		p.events = append(p.events[:0], p.events[over:]...)
	}
}

// SetPhase moves the run to phase.
func (p *Progress) SetPhase(phase string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.phase = phase
	p.current = -1
	p.mu.Unlock()
}

// MarkStopped records that the user asked the run to stop. The run itself
// ends through its canceled context; this only explains why.
func (p *Progress) MarkStopped() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.endedAt.IsZero() && !p.stopped {
		p.stopped = true
		p.appendEvent("warn", "Stop requested - keeping what is priced so far")
	}
	p.mu.Unlock()
}

// Finish ends the run. err is the run context's error, if any: a blown
// deadline is worth saying, a user stop already said so (MarkStopped).
func (p *Progress) Finish(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.endedAt.IsZero() {
		return
	}
	// Whatever a stopped or failed pass never reached keeps the fallback
	// ResolvePricing's finalize pass gave it; nothing is still in flight.
	if err == context.DeadlineExceeded {
		p.appendEvent("warn", "Ran out of time - the rest of the list kept its estimates")
	}
	p.endedAt = time.Now()
	p.phase = PhaseDone
	p.current = -1
	c := p.counts()
	p.appendEvent("info", fmt.Sprintf("Finished in %s: %d store, %d AI estimate, %d recipe estimate, %d unpriced",
		p.endedAt.Sub(p.startedAt).Round(time.Second), c.Store, c.AI, c.Guess, c.Unpriced+c.Pending))
}

// beginPass starts a resolve pass over names, one per shopping-list line.
// A run can make several (each budget-repair round prices the list again);
// the line states always describe the latest.
func (p *Progress) beginPass(names []string, stores int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.pass++
	p.phase = PhaseLookup
	p.current = -1
	p.lines = make([]ProgressLine, len(names))
	for i, n := range names {
		p.lines[i] = ProgressLine{Name: n, Status: LineWaiting}
	}
	p.appendEvent("info", fmt.Sprintf("Looking up %d item(s) across %d store(s)", len(names), stores))
	p.mu.Unlock()
}

// lineLooking marks line i as the one whose store lookups are in flight.
func (p *Progress) lineLooking(i int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if i < len(p.lines) {
		p.lines[i].Status = LineLooking
		p.current = i
	}
	p.mu.Unlock()
}

// lineQueued marks line i as a store miss waiting on the AI estimate.
func (p *Progress) lineQueued(i int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if i < len(p.lines) {
		p.lines[i].Status = LineQueued
		p.appendEvent("info", p.lines[i].Name+": no store price, queued for an AI estimate")
	}
	p.mu.Unlock()
}

// linePriced records where line i's price came from. cents is the pack price
// as resolved; lineFinal replaces it with the line total once units are
// reconciled.
func (p *Progress) linePriced(i int, how, source, storeName string, cents int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if i < len(p.lines) {
		ln := &p.lines[i]
		ln.Status, ln.How, ln.Source, ln.Store, ln.PriceCents = LinePriced, how, sourceLabel(how, source), storeName, cents
		msg := fmt.Sprintf("%s: $%.2f, %s", ln.Name, float64(cents)/100, ln.Source)
		if storeName != "" {
			msg += " at " + storeName
		}
		p.appendEvent("info", msg)
	}
	p.mu.Unlock()
}

// lineFinal records line i as written: its final source and line total, or
// unpriced when how is empty.
func (p *Progress) lineFinal(i int, how, source, storeName string, lineTotalCents int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if i < len(p.lines) {
		ln := &p.lines[i]
		if how == "" {
			*ln = ProgressLine{Name: ln.Name, Status: LineUnpriced, Source: "No price"}
			p.appendEvent("warn", ln.Name+": no price found")
		} else {
			ln.Status, ln.How, ln.Source, ln.Store, ln.PriceCents = LinePriced, how, sourceLabel(how, source), storeName, lineTotalCents
		}
	}
	p.mu.Unlock()
}

// sourceLabel names a price's origin for the dialog: how picks the group,
// source (a PriceResult.Source) the store tier within it.
func sourceLabel(how, source string) string {
	switch how {
	case HowAI:
		return "AI estimate"
	case HowGuess:
		return "Recipe estimate"
	}
	switch source {
	case "manual":
		return "Saved price"
	case "cache":
		return "Cached price"
	case "live":
		return "Live price"
	case "scrape":
		return "Store website"
	}
	return "Store price"
}

// counts needs p.mu held.
func (p *Progress) counts() ProgressCounts {
	var c ProgressCounts
	for _, ln := range p.lines {
		switch {
		case ln.Status == LineUnpriced:
			c.Unpriced++
		case ln.Status != LinePriced:
			c.Pending++
		case ln.How == HowAI:
			c.AI++
		case ln.How == HowGuess:
			c.Guess++
		default:
			c.Store++
		}
	}
	return c
}

// Snapshot copies the run's current state.
func (p *Progress) Snapshot() ProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	end := p.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	c := p.counts()
	return ProgressSnapshot{
		Kind:      p.kind,
		Phase:     p.phase,
		Status:    p.status(c),
		Active:    p.endedAt.IsZero(),
		Stopped:   p.stopped,
		StartedAt: p.startedAt,
		ElapsedMS: end.Sub(p.startedAt).Milliseconds(),
		Pass:      p.pass,
		Total:     len(p.lines),
		Done:      len(p.lines) - c.Pending,
		Counts:    c,
		Lines:     append([]ProgressLine(nil), p.lines...),
		Events:    append([]ProgressEvent(nil), p.events...),
	}
}

// status needs p.mu held.
func (p *Progress) status(c ProgressCounts) string {
	if p.stopped && p.endedAt.IsZero() {
		return "Stopping - finishing the lookup already in flight"
	}
	switch p.phase {
	case PhaseLookup:
		if p.current >= 0 && p.current < len(p.lines) {
			return "Checking store prices: " + p.lines[p.current].Name
		}
		return "Checking store prices"
	case PhaseAI:
		return fmt.Sprintf("Asking the AI to estimate %d item(s) no store had", c.Pending)
	case PhaseSaving:
		return "Working out pack sizes and saving prices"
	case PhaseRepair:
		return "Over budget - asking the AI for cheaper meals"
	case PhaseDone:
		if p.stopped {
			return "Stopped"
		}
		return "Finished"
	}
	return "Getting the list ready"
}
