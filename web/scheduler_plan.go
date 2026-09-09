package web

import (
	"context"
	"log"
	"time"
)

// lastAutoPlanCheck reports when this server's auto-plan scheduler last
// ticked (zero value if RunAutoPlanScheduler was never started, or hasn't
// ticked yet). Read by the About page's background-process list.
func (s *Server) lastAutoPlanCheck() time.Time {
	s.autoPlanMu.Lock()
	defer s.autoPlanMu.Unlock()
	return s.autoPlanCheckedAt
}

// RunAutoPlanScheduler ticks every 15 minutes and, once configured
// (cfg.AutoPlanHour >= 0), triggers an automatic plan generation the night
// before the week starts - Saturday at the household's local AutoPlanHour.
// plan.Generate always targets the next Sunday regardless of the
// WEEK_START_DAY setting (see plan/generate.go nextSunday), so the trigger
// day is fixed at Saturday to match. The existing "Regenerate"/"Plan my week"
// button already covers on-demand generation; this covers doing it
// automatically. Safe to run with no household configured yet or no LLM set -
// both are checked every tick and simply skip.
func (s *Server) RunAutoPlanScheduler(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maybeAutoGeneratePlan(ctx)
		}
	}
}

func (s *Server) maybeAutoGeneratePlan(ctx context.Context) {
	s.maybeAutoGeneratePlanAt(ctx, time.Now())
}

// maybeAutoGeneratePlanAt is maybeAutoGeneratePlan with an injectable clock,
// so the gating logic (enabled? right day/hour? not already generated? not
// already running?) can be unit tested without waiting on the real clock.
func (s *Server) maybeAutoGeneratePlanAt(ctx context.Context, wallClock time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("auto-plan: recovered from panic: %v", r)
		}
	}()

	s.autoPlanMu.Lock()
	s.autoPlanCheckedAt = wallClock
	s.autoPlanMu.Unlock()

	if s.cfg.AutoPlanHour < 0 || s.llmGen() == nil {
		return
	}
	hh, err := s.store.GetHousehold(ctx)
	if err != nil || hh == nil {
		return
	}

	loc := time.UTC
	if hh.Timezone != "" {
		if l, lerr := time.LoadLocation(hh.Timezone); lerr == nil {
			loc = l
		}
	}
	now := wallClock.In(loc)
	if now.Weekday() != time.Saturday || now.Hour() != s.cfg.AutoPlanHour {
		return
	}

	// The week plan.Generate will target always starts tomorrow (Sunday).
	targetWeekStart := now.AddDate(0, 0, 1).Format("2006-01-02")
	existing, err := s.store.ListPlansInRange(ctx, hh.ID, targetWeekStart, targetWeekStart)
	if err != nil {
		log.Printf("auto-plan: check existing plan for %s: %v", targetWeekStart, err)
		return
	}
	if len(existing) > 0 {
		return // already generated (manually, or by an earlier tick this same hour)
	}
	if s.jobs.Get(hh.ID) != nil {
		return // a generation is already in flight
	}

	log.Printf("auto-plan: starting scheduled generation for household %d, week of %s", hh.ID, targetWeekStart)
	s.startPlanGeneration(hh.ID)
}
