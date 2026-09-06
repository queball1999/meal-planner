package web

import (
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/csrf"

	"goeat/db"
	"goeat/middleware"
)

// pageData is the top-level value passed to every template execution.
type pageData struct {
	AppName   string
	Version   string
	Page      string       // active page slug for nav highlighting
	User      *db.User     // nil when not logged in
	Household *db.Household // nil when setup not yet completed
	CSRFField template.HTML // <input type="hidden"> for forms
	Flash     string       // one-shot flash message (cleared after display)
	Data      any          // page-specific data
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
		Flash:     s.popFlash(w, r),
		Data:      data,
	}

	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"icon": iconFunc,
			"divf": func(a int64, b float64) float64 { return float64(a) / b },
			"sub64": func(a, b int64) int64 { return a - b },
			"fmtWeekRange": func(start, end string) string {
				s, _ := time.Parse("2006-01-02", start)
				e, _ := time.Parse("2006-01-02", end)
				return s.Format("Jan 2") + " – " + e.Format("Jan 2")
			},
		}).
		ParseFS(templateFS,
			"templates/layout.html",
			"templates/"+name+".html",
		)
	if err != nil {
		log.Printf("render %s: parse: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout.html", pd); err != nil {
		log.Printf("render %s: execute: %v", name, err)
	}
}
