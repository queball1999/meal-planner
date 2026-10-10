package web

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"goeat/middleware"
	"goeat/settings"
)

// backgroundProcess is one row of the About page's "Background processes"
// table: what's actually running in this process, beyond the HTTP server
// itself, so an operator never has to go read main.go to find out.
type backgroundProcess struct {
	Name        string
	Description string
	Status      string // "ok" | "warn" | "off"
	StatusLabel string
	Detail      string
}

// aboutPageData is the About page's payload: build info plus a snapshot of
// live runtime/connectivity stats and background jobs.
type aboutPageData struct {
	AppName string
	Version string

	// VCSRevision/VCSTime/VCSModified come from debug.ReadBuildInfo(),
	// populated automatically for a binary built from within a git checkout -
	// no ldflags required. Empty when built outside one (e.g. `go install`
	// from a module cache).
	VCSRevision      string
	VCSRevisionShort string
	VCSTime          string
	VCSModified      bool
	GoVersion        string
	NumCPU           int

	Uptime       string
	NumGoroutine int
	MemAllocMB   float64
	MemSysMB     float64
	NumGC        uint32

	DBReachable bool
	DBError     string

	LLMConfigured bool
	LLMProvider   string
	LLMModel      string

	// Probes are the connectivity rows to render, in order, as placeholders.
	// Their status is filled in by /about/probe: each row is a real request to
	// the service, and running them during the page render would block the
	// whole page on whichever is slowest or down.
	Probes []probeResult

	Processes []backgroundProcess

	// Updates is the Updates card: is a newer release out (phase 15).
	Updates aboutUpdates
}

// handleAbout renders the About page: build/version info plus live runtime,
// connectivity, and background-job stats. Read-only - nothing here mutates
// state.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	// Household is optional (unlike most pages): the About page is useful for
	// diagnosing DB/LLM connectivity even before setup finishes. The plan
	// generation row just reads as idle with no household to key a job on.
	var householdID int64
	if hh := middleware.HouseholdFromCtx(r); hh != nil {
		householdID = hh.ID
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	s.render(w, r, "about", s.buildAboutData(ctx, householdID))
}

// buildAboutData assembles the About page's payload. The only two calls that
// can actually block (a DB ping and the household lookups used to size the
// background-process rows) are bounded by ctx.
func (s *Server) buildAboutData(ctx context.Context, householdID int64) aboutPageData {
	data := aboutPageData{
		AppName:   s.cfg.AppName,
		Version:   s.version,
		GoVersion: runtime.Version(),
		NumCPU:    runtime.NumCPU(),
		Uptime:    formatUptime(time.Since(s.startedAt)),
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				data.VCSRevision = setting.Value
				data.VCSRevisionShort = setting.Value
				if len(data.VCSRevisionShort) > 12 {
					data.VCSRevisionShort = data.VCSRevisionShort[:12]
				}
			case "vcs.time":
				data.VCSTime = setting.Value
			case "vcs.modified":
				data.VCSModified = setting.Value == "true"
			}
		}
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	data.NumGoroutine = runtime.NumGoroutine()
	data.MemAllocMB = float64(mem.Alloc) / (1 << 20)
	data.MemSysMB = float64(mem.Sys) / (1 << 20)
	data.NumGC = mem.NumGC

	if err := s.store.Ping(ctx); err != nil {
		data.DBError = err.Error()
	} else {
		data.DBReachable = true
	}

	// Reports what s.llmGen() actually is, not a live-resolved credential set: an
	// AI provider edit on the Settings page needs a restart to take effect
	// (see settings.Apply's own doc comment), so probing "live" settings here
	// could report a provider as reachable when it isn't the one actually
	// wired into plan generation.
	if s.llmGen() != nil {
		data.LLMConfigured = true
		data.LLMProvider = s.llmGen().ProviderName()
		data.LLMModel = s.llmGen().ModelName()
	}

	data.Probes = probePlaceholders()
	data.Processes = s.buildBackgroundProcesses(ctx, householdID)
	data.Updates = s.buildAboutUpdates(ctx)
	return data
}

// buildBackgroundProcesses lists every long-running goroutine main.go starts,
// in the order it starts them, plus whatever this particular household's
// on-demand plan-generation job is doing right now. A loop that was never
// started (no HA base URL/token, AUTO_PLAN_HOUR=-1) is reported as "off"
// rather than omitted, so a missing row and a stopped one never look
// identical to a reader who doesn't already know which loops this
// deployment is supposed to have.
func (s *Server) buildBackgroundProcesses(ctx context.Context, householdID int64) []backgroundProcess {
	procs := []backgroundProcess{
		{
			Name:        "HTTP server",
			Description: "Serves the web app.",
			Status:      "ok",
			StatusLabel: "Serving",
			Detail:      "listening on " + s.cfg.ListenAddr,
		},
	}

	hc := settings.LiveHAConfig(ctx, s.store, s.cfg, s.box)
	switch {
	case !hc.PullEnabled():
		reason := "HA_SYNC_INTERVAL_MINUTES=0"
		if !hc.Configured() {
			reason = "no Home Assistant base URL/token set"
		}
		procs = append(procs, backgroundProcess{
			Name: "Home Assistant sync", Description: "Pulls checked-off shopping list items back from Home Assistant.",
			Status: "off", StatusLabel: "Not configured", Detail: reason,
		})
	default:
		p := backgroundProcess{
			Name: "Home Assistant sync", Description: "Pulls checked-off shopping list items back from Home Assistant.",
			Status: "ok", StatusLabel: "Running",
			Detail: fmt.Sprintf("every %d min - %s", hc.IntervalMinutes, lastRunDetail(s.haScheduler.LastRun())),
		}
		if s.haScheduler.Running() {
			p.StatusLabel = "Syncing now"
		}
		procs = append(procs, p)
	}

	switch {
	case s.cfg.AutoPlanHour < 0:
		procs = append(procs, backgroundProcess{
			Name: "Auto-plan generation", Description: "Generates next week's plan automatically the night before it starts.",
			Status: "off", StatusLabel: "Not configured", Detail: "AUTO_PLAN_HOUR=-1",
		})
	case s.llmGen() == nil:
		procs = append(procs, backgroundProcess{
			Name: "Auto-plan generation", Description: "Generates next week's plan automatically the night before it starts.",
			Status: "warn", StatusLabel: "Waiting on AI provider", Detail: "AUTO_PLAN_HOUR is set but no LLM is configured",
		})
	default:
		procs = append(procs, backgroundProcess{
			Name: "Auto-plan generation", Description: "Generates next week's plan automatically the night before it starts.",
			Status: "ok", StatusLabel: "Running",
			Detail: fmt.Sprintf("%s at %02d:00 - %s", autoPlanDay(s.cfg.WeekStartDay), s.cfg.AutoPlanHour, lastRunDetail(s.lastAutoPlanCheck())),
		})
	}

	switch {
	case s.itemImageDir == "":
		procs = append(procs, backgroundProcess{
			Name: "Pantry photo backfill", Description: "Slowly discovers and downloads photos for catalog items that don't have one yet.",
			Status: "off", StatusLabel: "Not configured", Detail: "ITEM_IMAGE_DIR is not set",
		})
	default:
		procs = append(procs, backgroundProcess{
			Name: "Pantry photo backfill", Description: "Slowly discovers and downloads photos for catalog items that don't have one yet.",
			Status: "ok", StatusLabel: "Running",
			Detail: fmt.Sprintf("every %d min, one item at a time - %s",
				int(imageBackfillInterval.Minutes()), lastRunDetail(s.lastImageBackfillCheck())),
		})
	}

	renderCfg := settings.LiveRenderConfig(ctx, s.store, s.cfg)
	if !renderCfg.Enabled() {
		procs = append(procs, backgroundProcess{
			Name: "Scraping renderer", Description: "Headless browser used when a store blocks plain fetches or renders prices in JavaScript.",
			Status: "off", StatusLabel: "Not configured", Detail: "no FlareSolverr/Browserless URL set",
		})
	} else {
		var labels string
		for i, r := range renderCfg.Renderers() {
			if i > 0 {
				labels += ", "
			}
			labels += r.Label()
		}
		procs = append(procs, backgroundProcess{
			Name: "Scraping renderer", Description: "Headless browser used when a store blocks plain fetches or renders prices in JavaScript.",
			Status: "ok", StatusLabel: "Configured", Detail: labels,
		})
	}

	if job := s.jobs.Get(householdID); job != nil {
		procs = append(procs, backgroundProcess{
			Name: "Plan generation", Description: "This household's current meal plan, if one is being generated right now.",
			Status: "ok", StatusLabel: "Running", Detail: "in progress",
		})
	} else {
		procs = append(procs, backgroundProcess{
			Name: "Plan generation", Description: "This household's current meal plan, if one is being generated right now.",
			Status: "off", StatusLabel: "Idle", Detail: "no generation in progress",
		})
	}

	procs = append(procs, s.updateCheckProcess(ctx))
	return procs
}

// lastRunDetail renders a "last ran ..." phrase for a background loop, or
// says so plainly for the window before its first tick has landed.
func lastRunDetail(last time.Time) string {
	if last.IsZero() {
		return "no pass completed yet"
	}
	return "last checked " + humaniseSince(time.Since(last))
}

// humaniseSince renders a duration as a short "3d ago"-style phrase, coarsest
// unit only - exact seconds are noise once anything is more than a minute old.
func humaniseSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatUptime renders a duration as a short "3d 4h 12m" phrase, dropping
// leading zero units - "42s" reads better than "0d 0h 0m 42s" for a
// freshly-started process, which is exactly when this is most worth reading.
func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// autoPlanDay names the day the auto-plan scheduler fires on: the last day of
// the week under WEEK_START_DAY, the night before the next one starts.
func autoPlanDay(weekStartDay string) string {
	if weekStartDay == "monday" {
		return "Sundays"
	}
	return "Saturdays"
}
