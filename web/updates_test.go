package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goeat/config"
	"goeat/settings"
	"goeat/updatecheck"
)

// withRelease points the fixture's update checker at a fake GitHub whose
// latest release is tag, running version current, and runs one check.
func withRelease(t *testing.T, f *rbacFixture, current, tag string) {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name": "` + tag + `", "name": "Go Eat ` + tag + `",
			"html_url": "https://github.com/queball1999/meal-planner/releases/tag/` + tag + `",
			"published_at": "2026-10-01T12:00:00Z"}`))
	}))
	t.Cleanup(gh.Close)

	c := updatecheck.New(current)
	c.APIBase = gh.URL
	c.Client = gh.Client()
	c.Check(context.Background())
	f.srv.updates = c
}

func footerOf(t *testing.T, f *rbacFixture, user string) string {
	t.Helper()
	w := f.do(user, "GET", "/account", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /account as %s: %d", user, w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `<footer class="site-footer"`)
	if i < 0 {
		t.Fatalf("no footer for %s", user)
	}
	return body[i:]
}

// TestFooterFlagsUpdateForAdmins: "(update available)" shows for instance
// admins only, links to the GitHub release on a server, and goes away when
// UPDATE_CHECK is turned off.
func TestFooterFlagsUpdateForAdmins(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	footer := footerOf(t, f, "root")
	if !strings.Contains(footer, "(update available)") ||
		!strings.Contains(footer, `href="https://github.com/queball1999/meal-planner/releases/tag/v0.0.3"`) ||
		!strings.Contains(footer, `target="_blank"`) {
		t.Errorf("admin footer:\n%s", footer)
	}
	if footer := footerOf(t, f, "olga"); strings.Contains(footer, "update available") {
		t.Error("a household owner who isn't an instance admin sees the update flag")
	}

	if err := f.store.SetSetting(context.Background(), settings.UpdateCheckKey, "off"); err != nil {
		t.Fatal(err)
	}
	if footer := footerOf(t, f, "root"); strings.Contains(footer, "update available") {
		t.Error("flag still shown with UPDATE_CHECK off")
	}
}

func TestFooterQuietWhenUpToDate(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	withRelease(t, f, "v0.0.3", "v0.0.3")

	if footer := footerOf(t, f, "root"); strings.Contains(footer, "update available") {
		t.Error("flag shown when already on the latest release")
	}
}

// TestFooterOnDesktopLinksToAbout: desktop installs from About → Updates, so
// the footer goes there rather than off to GitHub.
func TestFooterOnDesktopLinksToAbout(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true; c.Desktop = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	footer := footerOf(t, f, "root")
	if !strings.Contains(footer, `href="/about#updates"`) || strings.Contains(footer, `target="_blank"`) {
		t.Errorf("desktop footer:\n%s", footer)
	}
}

func TestAboutUpdatesCard(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	w := f.do("root", "GET", "/about", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /about: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="updates"`, "v0.0.3", "update available", "published Oct 1, 2026",
		`action="/about/update-check"`, "pull the new Docker image",
		"Update check", // background-process row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("About page missing %q", want)
		}
	}
}

func TestAboutUpdatesCardDevBuild(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	f.srv.updates = updatecheck.New("dev")

	body := f.do("root", "GET", "/about", nil).Body.String()
	if !strings.Contains(body, "This is a development build (dev)") || strings.Contains(body, `action="/about/update-check"`) {
		t.Error("dev build should explain there's nothing to compare and offer no Check now")
	}
}

// TestUpdateCheckIsAdminOnly: Check now makes a request to GitHub on the
// server's behalf, so only an instance admin may trigger it.
func TestUpdateCheckIsAdminOnly(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	for _, user := range []string{"olga", "alice", "vera"} {
		if w := f.do(user, "POST", "/about/update-check", nil); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("%s: POST /about/update-check = %d, want refused", user, w.Code)
		}
	}
	w := f.do("root", "POST", "/about/update-check", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/about#updates" {
		t.Errorf("admin: %d to %q", w.Code, w.Header().Get("Location"))
	}
}

// TestAboutShowsBundledUpdater: a desktop build shows whether CI bundled the
// updater, and the hash it will check before starting it.
func TestAboutShowsBundledUpdater(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true; c.Desktop = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	if body := f.do("root", "GET", "/about", nil).Body.String(); !strings.Contains(body, "not bundled") {
		t.Error("desktop build without an updater should say so")
	}

	f.srv.SetBundledUpdater(strings.Repeat("ab", 32))
	body := f.do("root", "GET", "/about", nil).Body.String()
	if !strings.Contains(body, "bundled") || !strings.Contains(body, "<code>abababababab</code>") {
		t.Error("bundled updater hash missing from About")
	}
}

func TestServerAboutHidesUpdaterChip(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.UpdateCheck = true })
	withRelease(t, f, "v0.0.2", "v0.0.3")

	if body := f.do("root", "GET", "/about", nil).Body.String(); strings.Contains(body, "Updater</span>") {
		t.Error("a server has no bundled updater to report")
	}
}
