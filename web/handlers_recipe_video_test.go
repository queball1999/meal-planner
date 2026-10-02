package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"goeat/plan"
	"goeat/recipes"
	"goeat/video"
)

func TestVideoImportMessage(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("recipes: %w: The recipe is at the link in bio.", recipes.ErrNoRecipeInVideo), "link in their bio, import that page"},
		{fmt.Errorf("video: read TikTok post: [TikTok] 1: Video not available"), "read TikTok post: [TikTok] 1: Video not available"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), "longer than 10 minutes"},
	}
	for _, c := range cases {
		got := videoImportMessage(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("videoImportMessage(%v) = %q, want it to contain %q", c.err, got, c.want)
		}
		if strings.HasPrefix(got, "video: ") || strings.HasPrefix(got, "recipes: ") {
			t.Errorf("package prefix left in %q", got)
		}
	}
}

func TestVideoResultIsPerHouseholdAndClearable(t *testing.T) {
	s := &Server{videoLast: make(map[int64]plan.JobEvent)}
	s.setVideoResult(1, &plan.JobEvent{Type: "done", Message: "/recipes/7"})
	if got := s.videoResult(1); got == nil || got.Message != "/recipes/7" {
		t.Fatalf("household 1 result = %+v", got)
	}
	if s.videoResult(2) != nil {
		t.Error("household 2 must not see household 1's import")
	}
	s.setVideoResult(1, nil)
	if s.videoResult(1) != nil {
		t.Error("a new import should clear the last result")
	}
}

// TestRecipeImportPageBanner renders Import Recipe in each video-import state
// and checks the banner says the right thing to the right person.
func TestRecipeImportPageBanner(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/recipe_import.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		name    string
		admin   bool
		state   videoImportState
		want    []string
		notWant []string
	}{
		{"no AI", true, videoImportState{Capability: "full"},
			[]string{"Video links need an AI provider"}, []string{"data-video-banner"}},
		{"nothing installed, admin", true, videoImportState{HasLLM: true, Capability: "none", CanInstall: true, DownloadMB: 200},
			[]string{"can't be imported yet", "data-video-download", "~200 MB", "/static/js/video-tools.js"}, nil},
		{"nothing installed, member", false, videoImportState{HasLLM: true, Capability: "none", CanInstall: true},
			[]string{"can't be imported yet", "Ask an admin"}, []string{"data-video-download", "video-tools.js"}},
		{"captions only", true, videoImportState{HasLLM: true, Capability: "caption", CanInstall: true, Running: true},
			[]string{"Video import is limited", "data-video-running", "Captions only"}, nil},
		{"ready", true, videoImportState{HasLLM: true, Capability: "full"},
			[]string{"badge-success\">Ready"}, []string{"data-video-banner", "Video links need"}},
	}
	for _, c := range cases {
		var buf strings.Builder
		data := pageData{AppName: "Go Eat", IsAdmin: c.admin, Data: recipeImportPageData{Video: c.state}}
		if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
			t.Fatalf("%s: execute: %v", c.name, err)
		}
		out := buf.String()
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q", c.name, w)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(out, w) {
				t.Errorf("%s: should not contain %q", c.name, w)
			}
		}
	}
}

func TestVideoToolsInstallRejectsBadInput(t *testing.T) {
	tools := video.Tools{Dir: t.TempDir()}
	s := &Server{videoTools: tools, videoInstaller: video.NewInstaller(tools)}

	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/settings/video-tools/install", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleVideoToolsInstall(w, r)
		return w
	}
	if w := post(url.Values{"component": {"model"}, "model": {"huge-unknown"}}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown model: status %d", w.Code)
	}
	if w := post(url.Values{"component": {"rm -rf"}}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown component: status %d", w.Code)
	}
	if w := post(url.Values{}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"capability"`) {
		t.Errorf("nothing requested should just report status: %d %s", w.Code, w.Body.String())
	}
}
