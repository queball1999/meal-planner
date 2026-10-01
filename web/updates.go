package web

import (
	"context"
	"net/http"
	"time"

	"goeat/middleware"
	"goeat/settings"
)

// updateCheckTimeout bounds About → Updates' "Check now", which runs while
// the admin waits for the redirect.
const updateCheckTimeout = 20 * time.Second

// footerUpdate returns the newer version and where the footer should link,
// or "", "" when there's nothing to flag: no newer release, the viewer isn't
// an admin, or UPDATE_CHECK has been turned off since it was found.
func (s *Server) footerUpdate(r *http.Request) (latest, url string) {
	st := s.updates.Status()
	if !st.Available || st.Latest == nil || !middleware.UserFromCtx(r).IsAdmin() {
		return "", ""
	}
	// Only read when there's something to show, so an ordinary page load
	// costs no extra query.
	if !settings.LiveUpdateCheck(r.Context(), s.store, s.cfg) {
		return "", ""
	}
	if s.cfg.Desktop {
		return st.Latest.Version, "/about#updates"
	}
	return st.Latest.Version, st.Latest.URL
}

// aboutUpdates is About → Updates.
type aboutUpdates struct {
	Enabled    bool
	Comparable bool // false for dev builds: nothing to compare against
	Desktop    bool
	// UpdaterSHA256 is the bundled updater's hash on desktop ("" when this
	// build has none), shortened for display.
	UpdaterSHA256      string
	UpdaterSHA256Short string
	// InstallBlocker is why Install isn't offered ("" when it is); only
	// meaningful on desktop with an update available.
	InstallBlocker string
	Current        string
	Available      bool
	Checking       bool

	Latest      string
	LatestName  string
	LatestURL   string
	PublishedAt string // "Oct 1, 2026"
	CheckedAgo  string // "3h ago"; "" when it has never checked
	Err         string
}

func (s *Server) buildAboutUpdates(ctx context.Context) aboutUpdates {
	st := s.updates.Status()
	u := aboutUpdates{
		Enabled:       settings.LiveUpdateCheck(ctx, s.store, s.cfg),
		Comparable:    st.Comparable,
		Desktop:       s.cfg.Desktop,
		UpdaterSHA256: s.updaterSHA256,
		Current:       st.Current,
		Available:     st.Available,
		Checking:      st.Checking,
		Err:           st.Err,
	}
	u.InstallBlocker = s.installBlocker()
	if len(u.UpdaterSHA256) > 12 {
		u.UpdaterSHA256Short = u.UpdaterSHA256[:12]
	}
	if st.Latest != nil {
		u.Latest = st.Latest.Version
		u.LatestName = st.Latest.Name
		u.LatestURL = st.Latest.URL
		if !st.Latest.PublishedAt.IsZero() {
			u.PublishedAt = inAppTZ(st.Latest.PublishedAt).Format("Jan 2, 2006")
		}
	}
	if !st.CheckedAt.IsZero() {
		u.CheckedAgo = humaniseSince(time.Since(st.CheckedAt))
	}
	return u
}

// updateCheckProcess is the About page's background-process row.
func (s *Server) updateCheckProcess(ctx context.Context) backgroundProcess {
	p := backgroundProcess{
		Name:        "Update check",
		Description: "Asks GitHub twice a day whether a newer Go Eat release is out. Never downloads anything.",
	}
	st := s.updates.Status()
	switch {
	case !st.Comparable:
		p.Status, p.StatusLabel, p.Detail = "off", "Not checking", st.Current+" isn't a release version"
	case !settings.LiveUpdateCheck(ctx, s.store, s.cfg):
		p.Status, p.StatusLabel, p.Detail = "off", "Turned off", "UPDATE_CHECK=off"
	case st.Err != "":
		p.Status, p.StatusLabel, p.Detail = "warn", "Last check failed", st.Err
	default:
		p.Status, p.StatusLabel = "ok", "Running"
		p.Detail = "every 12h - " + lastRunDetail(st.CheckedAt)
		if st.Checking {
			p.StatusLabel = "Checking now"
		}
	}
	return p
}

// handleUpdateCheck is About → Updates' "Check now". Runs even with
// UPDATE_CHECK off: an admin asking explicitly is the point of the switch.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), updateCheckTimeout)
	defer cancel()

	st := s.updates.Check(ctx)
	switch {
	case !st.Comparable:
		s.setNotify(w, NotifyInfo, "This is a development build ("+st.Current+"), so there's no release to compare it with.")
	case st.Err != "":
		s.setNotify(w, NotifyDanger, "Couldn't check for updates: "+st.Err)
	case st.Available:
		s.setNotify(w, NotifySuccess, st.Latest.Version+" is available.")
	case st.Latest == nil:
		s.setNotify(w, NotifyInfo, "No release has been published yet.")
	default:
		s.setNotify(w, NotifySuccess, "You're on the latest version ("+st.Current+").")
	}
	http.Redirect(w, r, "/about#updates", http.StatusSeeOther)
}
