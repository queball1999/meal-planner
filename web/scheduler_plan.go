package web

import (
	"context"
	"log"
	"time"

	"goeat/db"
	"goeat/plan"
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
// before the week starts: the last day of the week under WEEK_START_DAY
// (Saturday for a Sunday week, Sunday for a Monday week) at the household's
// local AutoPlanHour, for the week starting the next morning. The existing
// "Regenerate"/"Plan my week" button already covers on-demand generation;
// this covers doing it automatically. Safe to run with no household configured yet or no LLM set -
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
	households, err := s.store.ListHouseholds(ctx)
	if err != nil {
		log.Printf("auto-plan: list households: %v", err)
		return
	}
	for _, hh := range households {
		s.maybeAutoGenerateFor(ctx, hh, wallClock)
	}
}

// maybeAutoGenerateFor is one household's turn of the auto-plan tick. Each
// household is judged in its own timezone, so "the night before the week
// starts" lands at a different instant for each.
func (s *Server) maybeAutoGenerateFor(ctx context.Context, hh *db.Household, wallClock time.Time) {
	loc := time.UTC
	if hh.Timezone != "" {
		if l, lerr := time.LoadLocation(hh.Timezone); lerr == nil {
			loc = l
		}
	}
	now := wallClock.In(loc)
	if now.Hour() != s.cfg.AutoPlanHour {
		return
	}
	// The household's calendar date, as the midnight-UTC day plan.WeekBounds
	// works in - not the instant, which near midnight is a different date in
	// UTC than it is at the household's kitchen table.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if _, weekEnd := plan.WeekBounds(today, s.cfg.WeekStartDay); !today.Equal(weekEnd) {
		return // not the night before a week starts
	}
	weekStart := today.AddDate(0, 0, 1)
	targetWeekStart := weekStart.Format("2006-01-02")
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
	s.startPlanGenerationForWeek(hh.ID, weekStart, weekStart, nil, nil, nil)
}
