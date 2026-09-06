package web

import "html/template"

// aiProviderLogo returns the mark shown beside a provider in the Settings
// picker. These are brand-coloured letter marks rather than the vendors'
// actual logotypes - close enough to tell the four options apart at a glance
// without shipping trademarked artwork.
func aiProviderLogo(id string) template.HTML {
	var bg, fg, glyph string
	switch id {
	case "anthropic":
		bg, fg, glyph = "#D97757", "#FFFFFF", "A"
	case "openai":
		bg, fg, glyph = "#10A37F", "#FFFFFF", "O"
	case "google":
		bg, fg, glyph = "#4285F4", "#FFFFFF", "G"
	default:
		bg, fg, glyph = "#64748B", "#FFFFFF", "{}"
	}
	size := "16"
	if glyph == "{}" {
		size = "11"
	}
	return template.HTML(
		`<svg class="ai-logo" viewBox="0 0 28 28" width="28" height="28" aria-hidden="true">` +
			`<rect width="28" height="28" rx="7" fill="` + bg + `"/>` +
			`<text x="14" y="14" fill="` + fg + `" font-size="` + size + `" font-weight="700" ` +
			`font-family="system-ui,sans-serif" text-anchor="middle" dominant-baseline="central">` +
			glyph + `</text></svg>`)
}
