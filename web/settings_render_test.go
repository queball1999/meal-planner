package web

import (
	"html/template"
	"strings"
	"testing"
)

// TestSettingsPageExecutes renders the settings page against a filled-in
// settingsPageData. Parsing alone would not catch a field referenced from the
// wrong scope inside the provider loop - that only fails at execution time.
func TestSettingsPageExecutes(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/settings.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	key := settingsFieldView{Key: "OPENAI_API_KEY", Role: "key", Label: "API key", Kind: "secret", IsSet: true, Source: "admin"}
	model := settingsFieldView{Key: "OPENAI_MODEL", Role: "model", Label: "Model", Kind: "string", Value: "gpt-5"}
	url := settingsFieldView{Key: "OPENAI_API_URL", Role: "url", Label: "API URL", Kind: "string", Value: "https://api.openai.com/v1"}

	data := pageData{
		AppName: "Go Eat",
		Data: settingsPageData{
			HasLLM:   true,
			ActiveID: "openai",
			Providers: []aiProviderView{{
				ID: "openai", Label: "OpenAI", ShowURL: true, IsDefault: true, Configured: true,
				DocsURL:  "https://platform.openai.com/api-keys",
				Models:   []string{"gpt-5", "gpt-4.1"},
				KeyField: &key, ModelField: &model, URLField: &url,
			}, {
				ID: "anthropic", Label: "Anthropic",
			}},
			SharedFields: []settingsFieldView{
				{Key: "LLM_TEMPERATURE", Label: "Temperature", Kind: "float", Value: "1.01"},
				{Key: "LLM_TOP_K", Label: "Top K", Kind: "int", Value: "20"},
			},
			Categories: []settingsCategoryView{{
				Name:   "General",
				Fields: []settingsFieldView{{Key: "APP_NAME", Label: "App name", Kind: "string", Value: "Go Eat"}},
			}},
		},
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		`data-provider-panel="openai"`,
		`data-provider-panel="anthropic"`, // every provider is rendered, not just the active one
		`data-setting-key="OPENAI_MODEL"`,
		`data-setting-key="LLM_TOP_K"`,
		`value="gpt-4.1"`, // curated models reach the dropdown
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered settings page missing %q", want)
		}
	}
}
