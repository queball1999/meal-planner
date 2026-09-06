package web

import (
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestAllTemplatesParse parses every page template exactly the way render()
// does. It catches a template that references a func or partial the real
// FuncMap does not provide - which would otherwise only surface as a 500 on
// the page nobody opened before shipping.
func TestAllTemplatesParse(t *testing.T) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil || len(names) == 0 {
		t.Fatalf("no templates found: %v", err)
	}
	for _, n := range names {
		base := strings.TrimSuffix(filepath.Base(n), ".html")
		if base == "layout" {
			continue
		}
		if _, err := template.New("").
			Funcs(templateFuncs()).
			ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", n); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}
