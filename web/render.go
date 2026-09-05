package web

import (
	"html/template"
	"log"
	"net/http"
)

// pageData is the top-level value passed to every template.
type pageData struct {
	AppName string
	Version string
	Page    string // active page key for nav highlighting
	Data    any
}

// render parses layout.html + the named page template and executes them.
// Page templates live at web/templates/<name>.html and define a "content"
// block consumed by layout.html.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	pd := pageData{
		AppName: s.cfg.AppName,
		Version: s.version,
		Page:    name,
		Data:    data,
	}

	tmpl, err := template.New("").
		Funcs(template.FuncMap{"icon": iconFunc}).
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
		// Headers already sent; log but can't change status.
		log.Printf("render %s: execute: %v", name, err)
	}
}
