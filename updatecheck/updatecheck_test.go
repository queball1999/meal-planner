package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.0.3", "v0.0.2", true},
		{"v0.1.0", "v0.0.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.0.2", "v0.0.2", false},
		{"v0.0.1", "v0.0.2", false},
		{"v0.0.2", "v0.0.2-dev", true}, // a dev build of 0.0.2 predates 0.0.2
		{"v0.0.2-dev", "v0.0.2", false},
		{"0.0.3", "v0.0.2", true},   // tag without the v
		{"v0.0.10", "v0.0.9", true}, // numeric, not string, order
		{"v0.0.3", "dev", false},
		{"v0.0.3", "dev-abc1234", false},
		{"nightly", "v0.0.2", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

// fakeGitHub serves /repos/<repo>/releases/latest with the given status and body.
func fakeGitHub(t *testing.T, status int, body string) (*Checker, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/"+DefaultRepo+"/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "GoEat/") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := New("v0.0.2")
	c.APIBase = srv.URL
	c.Client = srv.Client()
	return c, &calls
}

func TestCheckFindsNewerRelease(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusOK, `{
		"tag_name": "v0.0.3", "name": "Go Eat v0.0.3",
		"html_url": "https://github.com/queball1999/meal-planner/releases/tag/v0.0.3",
		"published_at": "2026-10-01T12:00:00Z", "draft": false, "prerelease": false}`)

	st := c.Check(context.Background())

	if st.Err != "" || !st.Available || st.Latest == nil || st.Latest.Version != "v0.0.3" {
		t.Fatalf("status = %+v", st)
	}
	if st.Latest.URL != "https://github.com/queball1999/meal-planner/releases/tag/v0.0.3" {
		t.Errorf("URL = %q", st.Latest.URL)
	}
	if st.Latest.PublishedAt.IsZero() || st.CheckedAt.IsZero() {
		t.Errorf("times not set: %+v", st)
	}
}

func TestCheckUpToDate(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusOK, `{"tag_name": "v0.0.2", "html_url": "https://github.com/queball1999/meal-planner/releases/tag/v0.0.2"}`)

	if st := c.Check(context.Background()); st.Available || st.Err != "" || st.Latest == nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheckNothingPublished(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusNotFound, `{"message": "Not Found"}`)

	if st := c.Check(context.Background()); st.Available || st.Err != "" || st.Latest != nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheckRebuildsForeignReleaseURL(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusOK, `{"tag_name": "v0.0.3", "html_url": "https://evil.example/download"}`)

	st := c.Check(context.Background())
	if st.Latest == nil || st.Latest.URL != "https://github.com/queball1999/meal-planner/releases/tag/v0.0.3" {
		t.Fatalf("status = %+v", st)
	}
}

func TestFailedCheckKeepsLastKnownRelease(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusOK, `{"tag_name": "v0.0.3"}`)
	c.Check(context.Background())

	c.APIBase = "http://127.0.0.1:1" // nothing listens there
	st := c.Check(context.Background())

	if st.Err == "" {
		t.Fatal("expected an error")
	}
	if !st.Available || st.Latest == nil || st.Latest.Version != "v0.0.3" {
		t.Fatalf("lost the last known release: %+v", st)
	}
}

func TestCheckRateLimited(t *testing.T) {
	c, _ := fakeGitHub(t, http.StatusForbidden, `{"message": "API rate limit exceeded"}`)

	if st := c.Check(context.Background()); !strings.Contains(st.Err, "rate limit") {
		t.Fatalf("Err = %q", st.Err)
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	c, calls := fakeGitHub(t, http.StatusOK, `{"tag_name": "v9.9.9"}`)
	c.status = New("dev").status

	st := c.Check(context.Background())

	if *calls != 0 || st.Comparable || st.Available || !st.CheckedAt.IsZero() {
		t.Fatalf("calls = %d, status = %+v", *calls, st)
	}
}
