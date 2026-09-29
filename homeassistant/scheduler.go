package homeassistant

import (
	"context"
	"log"
	"sync"
	"time"

	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
	"goeat/settings"
)

// Scheduler runs the background HA pull loop. It is the app's first recurring
// ticker; keep it defensive - a panic or a hung HA must never take the process
// down or block the next tick.
type Scheduler struct {
	store db.Store
	cfg   *config.Config
	box   *cryptbox.Box

	mu      sync.Mutex
	running bool
	lastRun time.Time
}

func NewScheduler(store db.Store, cfg *config.Config, box *cryptbox.Box) *Scheduler {
	return &Scheduler{store: store, cfg: cfg, box: box}
}

// LastRun reports when the scheduler last completed a sync pass (zero value
// if none has run yet). Used by the About page's background-process list.
func (s *Scheduler) LastRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRun
}

// Running reports whether a sync pass is in flight right now.
func (s *Scheduler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Run ticks every minute until ctx is cancelled. Each tick it re-reads the live
// config; when the pull interval has elapsed it runs a full sync pass.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maybeSync(ctx)
		}
	}
}

func (s *Scheduler) maybeSync(ctx context.Context) {
	hc := settings.LiveHAConfig(ctx, s.store, s.cfg, s.box)
	if !hc.PullEnabled() {
		return
	}

	s.mu.Lock()
	if s.running || time.Since(s.lastRun) < time.Duration(hc.IntervalMinutes)*time.Minute {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("ha sync: recovered from panic: %v", r)
		}
		s.mu.Lock()
		s.running = false
		s.lastRun = time.Now()
		s.mu.Unlock()
	}()

	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	if _, err := SyncOnce(runCtx, s.store, s.cfg, s.box); err != nil {
		log.Printf("ha sync: %v", err)
	}
}

// SyncOnce runs a pull then a push for the household. Shared by the scheduler
// and the on-demand "Sync now" button.
func SyncOnce(ctx context.Context, store db.Store, cfg *config.Config, box *cryptbox.Box) (Result, error) {
	var res Result
	hc := settings.LiveHAConfig(ctx, store, cfg, box)
	if !hc.Configured() {
		return res, errNotConfigured
	}
	client := NewClient(hc.BaseURL, hc.Token)
	if client == nil {
		return res, errNotConfigured
	}
	hhID, err := hc.HouseholdFor(ctx, store)
	if err != nil || hhID == 0 {
		return res, err
	}
	hh, err := store.GetHousehold(ctx, hhID)
	if err != nil || hh == nil {
		return res, err
	}

	pull, perr := PullList(ctx, store, client, hc, hh)
	push, uerr := PushList(ctx, store, client, hc, hh)

	res = Result{
		Pushed:       push.Pushed,
		Removed:      push.Removed,
		Checked:      pull.Checked,
		Unchecked:    pull.Unchecked,
		CheckedIDs:   pull.CheckedIDs,
		UncheckedIDs: pull.UncheckedIDs,
	}
	if perr != nil {
		return res, perr
	}
	return res, uerr
}
