package web

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	csrf "filippo.io/csrf/gorilla"

	"goeat/db"
	"goeat/middleware"
)

// pageData is the top-level value passed to every template execution.
type pageData struct {
	AppName   string
	Version   string
	Page      string        // active page slug for nav highlighting
	User      *db.User      // nil when not logged in
	Household *db.Household // active household; nil when the user has none
	// HouseholdRole is the user's effective role in Household; CanEdit and
	// CanOwn derive from it. For hiding controls only - the route gates in
	// routes.go are what enforce.
	HouseholdRole string
	CanEdit       bool
	CanOwn        bool
	IsAdmin       bool
	// Households is every household the user can switch to (switcher menu).
	Households []*db.HouseholdMembership
	CSRFField  template.HTML // <input type="hidden"> for forms
	CSRFToken  string        // raw token for JS fetch calls
	Notify     string        // one-shot notification message (cleared after display)
	NotifyKind string        // "success" | "info" | "warning" | "danger"
	Data       any           // page-specific data

	// WeekParam is the week the current request is explicitly viewing -
	// ?week= on /plan and /plan/list, ?calref= on the dashboard - carried
	// into the top nav (partials/nav_items.html) so switching between
	// Dashboard/Plan/Shopping List while looking at a non-default week keeps
	// showing that same week instead of snapping back to today/latest.
	// Empty whenever the request itself landed on a page with no such param,
	// so the nav links stay bare and still resolve to "today" by default.
	WeekParam string

	// Footer timing (§ middleware.Timing). PageLoadMs is wall-clock from the
	// top of the middleware chain to the start of template work; TemplateMs
	// is ParseFS + execute, timed by rendering once to io.Discard before the
	// real render - the only way to know the duration before it's over.
	PageLoadMs float64
	TemplateMs float64
}

// leadingStepNum matches an ordinal prefix the LLM (or an imported recipe)
// sometimes bakes into a step string: "1. ", "2) ", "3 - ", "Step 4: ".
// The templates render steps inside <ol>, which supplies its own number, so
// the baked-in one has to come off or every step reads "1. 1. …".
var leadingStepNum = regexp.MustCompile(`^\s*(?:[Ss]tep\s*)?\d+\s*[.)\-:]\s+`)

func stripStepNumber(s string) string {
	return strings.TrimSpace(leadingStepNum.ReplaceAllString(s, ""))
}

// formatDurationMs renders a millisecond duration the way the footer wants it:
// sub-10ms keeps one decimal (render times are often well under 1ms), whole
// milliseconds otherwise, and seconds past 1000ms.
func formatDurationMs(ms float64) string {
	switch {
	case ms >= 1000:
		return fmt.Sprintf("%.2fs", ms/1000)
	case ms >= 10:
		return fmt.Sprintf("%.0fms", ms)
	default:
		return fmt.Sprintf("%.1fms", ms)
	}
}

// toFloat coerces a numeric template value (int, int64, float64, or a
// numeric string) to float64 so the pct helper can compare values of mixed
// types without the caller casting. Non-numeric input yields 0.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// templateFuncs is the function map every page template is parsed with. It
// lives in one place so the template parse test can use the exact same map -
// a func added here can never go missing from the test and blow up at runtime.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"icon":     iconFunc,
		"stepText": stripStepNumber,
		// qtyLabel renders "4 each" as "4", "2 slice" as "2 slices", and
		// leaves "1.5 kg" alone. See web/format.go.
		"qtyLabel": qtyLabel,
		// dict builds an inline map so a shared partial can be called with
		// named arguments - html/template's {{template}} takes a single
		// pipeline, and partials/modal_open.html needs ID/Title/width.
		// An odd argument count or a non-string key is a template author's
		// typo, so it fails the render rather than silently dropping a pair.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd argument count (%d)", len(kv))
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is %T, want string", i, kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"slots": func() []string { return []string{"breakfast", "lunch", "dinner"} },
		// unitOptions is the shared unit picker list (each, g, oz, lb, …) used
		// by the partials/unit_select.html dropdown everywhere an item's unit is
		// edited. unitOptionsFor keeps an item's own unusual stock unit in the
		// list (and first) so the <select> can still show it selected.
		"unitOptions":    func() []string { return commonUnits },
		"unitOptionsFor": unitOptionsFor,
		"titleCase": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		// pill "source"|"unit"|"category" <value> -> the badge modifier that
		// gives that value its color. See web/pills.go.
		"pill":       pillClass,
		"storeLogo":  StoreLogoURL,
		"aiLogo":     aiProviderLogo,
		"ctxCookies": storeContextCookies,
		"ctxPrewarm": storeContextPrewarm,
		"ctxWaitFor": storeContextWaitFor,
		"join":       strings.Join,
		"divf":       func(a int64, b float64) float64 { return float64(a) / b },
		"sub64":      func(a, b int64) int64 { return a - b },
		// pct returns value/max as a 0-100 percentage for bar widths; a
		// non-positive max yields 0 so a zero total can't divide by zero.
		// Accepts any numeric type (int or int64) so callers need no casting.
		"pct": func(value, max any) int {
			v := toFloat(value)
			m := toFloat(max)
			if m <= 0 {
				return 0
			}
			p := int(v / m * 100)
			if p < 0 {
				p = 0
			}
			if p > 100 {
				p = 100
			}
			return p
		},
		// maxWeek returns the largest of any week's actual/estimate cents, so a
		// bar chart can scale every bar against one shared axis.
		"maxWeek": func(weeks []financeWeekRow) int64 {
			var max int64
			for _, w := range weeks {
				if w.ActualCents > max {
					max = w.ActualCents
				}
				if w.EstimateCents > max {
					max = w.EstimateCents
				}
			}
			return max
		},
		// maxDaily returns the largest daily run count, for the AI trend chart.
		"maxDaily": func(days []financeDailyRow) int64 {
			var max int64
			for _, d := range days {
				if int64(d.RunCount) > max {
					max = int64(d.RunCount)
				}
			}
			return max
		},
		"dur": formatDurationMs,
		"truncate": func(s string, n int) string {
			r := []rune(s)
			if len(r) <= n {
				return s
			}
			return string(r[:n]) + "…"
		},
		"fmtWeekRange": func(start, end string) string {
			s, _ := time.Parse("2006-01-02", start)
			e, _ := time.Parse("2006-01-02", end)
			return s.Format("Jan 2") + " - " + e.Format("Jan 2")
		},
	}
}

// requestWeekParam reads the week a request explicitly asked to view -
// /plan and /plan/list's ?week=, or the dashboard's ?calref= - so the top
// nav (partials/nav_items.html) can carry it into the Dashboard/Plan/
// Shopping List links. Only an explicit param counts: a bare "/plan" with
// neither means the request itself fell back to today/latest, and the nav
// should keep offering that same bare, always-current link rather than
// pinning to whatever week that fallback happened to resolve to.
func requestWeekParam(r *http.Request) string {
	if v := r.URL.Query().Get("week"); v != "" {
		return v
	}
	return r.URL.Query().Get("calref")
}

// render parses layout.html + the named page template and executes them.
// Page templates live at web/templates/<name>.html and must define a
// "content" block consumed by layout.html.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.renderWithPage(w, r, name, name, data)
}

// renderWithPage is render() with an explicit nav slug, for pages whose
// template name and active-nav key differ (e.g. /plan renders "plan" but its
// "Shopping list" sub-tab wants the nav key "list").
func (s *Server) renderWithPage(w http.ResponseWriter, r *http.Request, name, pageSlug string, data any) {
	pd := pageData{
		AppName:   s.cfg.AppName,
		Version:   s.version,
		Page:      pageSlug,
		User:      middleware.UserFromCtx(r),
		Household: middleware.HouseholdFromCtx(r),

		HouseholdRole: middleware.HouseholdRoleFromCtx(r),
		CanEdit:       middleware.CanEdit(r),
		CanOwn:        middleware.CanOwn(r),
		IsAdmin:       middleware.UserFromCtx(r).IsAdmin(),
		Households:    middleware.MembershipsFromCtx(r),
		CSRFField:     csrf.TemplateField(r),
		CSRFToken:     csrf.Token(r),
		Data:          data,
		WeekParam:     requestWeekParam(r),
	}
	pd.NotifyKind, pd.Notify = s.popNotify(w, r)
	if start, ok := middleware.RequestStart(r); ok {
		pd.PageLoadMs = float64(time.Since(start)) / float64(time.Millisecond)
	}

	templateStart := time.Now()
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS,
			"templates/layout.html",
			"templates/partials/*.html",
			"templates/"+name+".html",
		)
	if err != nil {
		log.Printf("render %s: parse: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Render once to io.Discard so TemplateMs (parse + execute) is known
	// before the real execute call below - the value has to already be on
	// pd for the footer to show it.
	if err := tmpl.ExecuteTemplate(io.Discard, "layout.html", pd); err != nil {
		log.Printf("render %s: execute (discard): %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pd.TemplateMs = float64(time.Since(templateStart)) / float64(time.Millisecond)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout.html", pd); err != nil {
		log.Printf("render %s: execute: %v", name, err)
	}
}

// renderFragment executes one named partial template directly, without
// layout.html - for a piece of a page a client polls and swaps in place
// (the shopping list tab's live pricing refresh) rather than a full
// navigation. pageSlug only matters for helpers the partial's markup reads
// off pageData (e.g. settingsTabs' active-tab highlight); most fragments
// don't need it.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, templateName, pageSlug string, data any) {
	pd := pageData{
		AppName:   s.cfg.AppName,
		Version:   s.version,
		Page:      pageSlug,
		User:      middleware.UserFromCtx(r),
		Household: middleware.HouseholdFromCtx(r),
		CSRFField: csrf.TemplateField(r),
		CSRFToken: csrf.Token(r),
		Data:      data,
	}

	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/partials/*.html")
	if err != nil {
		log.Printf("render fragment %s: parse: %v", templateName, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, templateName, pd); err != nil {
		log.Printf("render fragment %s: execute: %v", templateName, err)
	}
}
