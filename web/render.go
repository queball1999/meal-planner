package web

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/csrf"

	"goeat/db"
	"goeat/middleware"
)

// pageData is the top-level value passed to every template execution.
type pageData struct {
	AppName    string
	Version    string
	Page       string        // active page slug for nav highlighting
	User       *db.User      // nil when not logged in
	Household  *db.Household // nil when setup not yet completed
	CSRFField  template.HTML // <input type="hidden"> for forms
	CSRFToken  string        // raw token for JS fetch calls
	Notify     string        // one-shot notification message (cleared after display)
	NotifyKind string        // "success" | "info" | "warning" | "danger"
	Data       any           // page-specific data

	// Footer timing (§ middleware.Timing). PageLoadMs is wall-clock from the
	// top of the middleware chain to the start of template work; TemplateMs
	// is ParseFS + execute, timed by rendering once to io.Discard before the
	// real render - the only way to know the duration before it's over.
	PageLoadMs float64
	TemplateMs float64
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

// templateFuncs is the function map every page template is parsed with. It
// lives in one place so the template parse test can use the exact same map -
// a func added here can never go missing from the test and blow up at runtime.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"icon":       iconFunc,
		"storeLogo":  StoreLogoURL,
		"aiLogo":     aiProviderLogo,
		"ctxCookies": storeContextCookies,
		"ctxPrewarm": storeContextPrewarm,
		"ctxWaitFor": storeContextWaitFor,
		"join":       strings.Join,
		"divf":       func(a int64, b float64) float64 { return float64(a) / b },
		"sub64":      func(a, b int64) int64 { return a - b },
		"dur":        formatDurationMs,
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

// render parses layout.html + the named page template and executes them.
// Page templates live at web/templates/<name>.html and must define a
// "content" block consumed by layout.html.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	pd := pageData{
		AppName:   s.cfg.AppName,
		Version:   s.version,
		Page:      name,
		User:      middleware.UserFromCtx(r),
		Household: middleware.HouseholdFromCtx(r),
		CSRFField: csrf.TemplateField(r),
		CSRFToken: csrf.Token(r),
		Data:      data,
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
