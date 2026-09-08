package web

import (
	"net/http"
	"strings"

	"goeat/db"
)

// adminEventRow is one row of the Settings → "Admin events" viewer - db.AppEventRow
// plus the couple of strings the template wants pre-joined rather than
// re-deriving from three separate fields on every row.
type adminEventRow struct {
	*db.AppEventRow
	At     string // "Jan 2, 2006 3:04pm"
	Target string // "store #4", or "" when the event has no single target
}

type adminEventsPageData struct {
	Events []adminEventRow
	Status string // "" | "ok" | "error" - current filter
	Page   Pagination
}

// handleAdminEventsPage lists the app-wide audit log (auth, account, store,
// preferences, and settings changes - everything s.logEvent records) as its
// own Settings sub-tab. Distinct from /admin/llm-log ("Audit log"), which is
// AI call traffic specifically; this is who-did-what-to-the-app.
//
//	GET /admin/events
func (s *Server) handleAdminEventsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	rows, err := s.store.ListEvents(ctx, 500)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Couldn't load the admin event log.")
		rows = nil
	}

	events := make([]*db.AppEventRow, 0, len(rows))
	for _, e := range rows {
		if status != "" && e.Status != status {
			continue
		}
		events = append(events, e)
	}

	paged, page := paginate(r, events)
	view := make([]adminEventRow, len(paged))
	for i, e := range paged {
		target := ""
		if e.TargetType != "" {
			target = e.TargetType
			if e.TargetID != "" {
				target += " #" + e.TargetID
			}
		}
		view[i] = adminEventRow{
			AppEventRow: e,
			At:          e.OccurredAt.Format("Jan 2, 2006 3:04pm"),
			Target:      target,
		}
	}

	s.render(w, r, "admin_events", adminEventsPageData{Events: view, Status: status, Page: page})
}
