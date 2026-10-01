// Package updatecheck asks GitHub whether a newer stable Go Eat release is
// out (phase 15). It only reads release metadata - it never downloads or
// installs anything; on desktop, installing is QUpdateTool's job.
//
// The check runs in the server rather than in the updater because servers
// need the "update available" flag too, and they don't ship an updater.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRepo is where Go Eat releases are published.
	DefaultRepo = "queball1999/meal-planner"
	// DefaultAPIBase is GitHub's REST API.
	DefaultAPIBase = "https://api.github.com"

	// Interval between checks. GitHub allows 60 unauthenticated requests an
	// hour per IP; twice a day is nowhere near that, even for a household
	// running several installs behind one address.
	Interval = 12 * time.Hour
	// StartDelay keeps the first check out of startup, where it would race
	// migrations and seeding for no benefit.
	StartDelay = time.Minute

	requestTimeout  = 15 * time.Second
	maxResponseSize = 1 << 20
)

// Release is the newest stable release, as far as the last check knows.
type Release struct {
	Version     string    // the tag, e.g. "v0.0.3"
	Name        string    // "Go Eat v0.0.3"
	URL         string    // its GitHub release page
	PublishedAt time.Time // zero if GitHub didn't say
}

// Status is a snapshot of what the checker knows. The zero value means
// "never checked".
type Status struct {
	Current string // the running version
	// Comparable is false for builds that aren't a release version (dev,
	// dev-abc1234): there is nothing to compare, so no check runs.
	Comparable bool
	Latest     *Release // nil until a check succeeds, or when nothing is published
	Available  bool     // Latest is newer than Current
	CheckedAt  time.Time
	Err        string // why the last check failed; "" when it worked
	Checking   bool
}

// Checker fetches and remembers the latest release. Safe for concurrent use.
type Checker struct {
	Repo    string
	APIBase string
	Client  *http.Client

	mu     sync.Mutex
	status Status
}

// New returns a Checker for the running version, pointed at GitHub.
func New(current string) *Checker {
	_, comparable := Parse(current)
	return &Checker{
		Repo:    DefaultRepo,
		APIBase: DefaultAPIBase,
		Client:  &http.Client{Timeout: requestTimeout},
		status:  Status{Current: current, Comparable: comparable},
	}
}

// Status returns the latest snapshot.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Check asks GitHub now and returns the new status. A failed check keeps the
// last known release, so a flaky network doesn't hide an update that was
// already found.
func (c *Checker) Check(ctx context.Context) Status {
	c.mu.Lock()
	if !c.status.Comparable {
		c.mu.Unlock()
		return c.Status()
	}
	c.status.Checking = true
	c.mu.Unlock()

	latest, err := c.fetchLatest(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Checking = false
	c.status.CheckedAt = time.Now()
	if err != nil {
		c.status.Err = err.Error()
		return c.status
	}
	c.status.Err = ""
	c.status.Latest = latest
	c.status.Available = latest != nil && Newer(latest.Version, c.status.Current)
	return c.status
}

// Run checks after StartDelay and then every Interval until ctx ends.
// enabled is asked before every check (the UPDATE_CHECK setting), so turning
// the check off or on takes effect without a restart.
func (c *Checker) Run(ctx context.Context, enabled func(context.Context) bool) {
	if !c.Status().Comparable {
		log.Printf("update check: %q isn't a release version, not checking", c.Status().Current)
		return
	}

	timer := time.NewTimer(StartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if enabled(ctx) {
			st := c.Check(ctx)
			switch {
			case st.Err != "":
				log.Printf("update check: %s", st.Err)
			case st.Available:
				log.Printf("update check: %s is available (running %s)", st.Latest.Version, st.Current)
			}
		}
		timer.Reset(Interval)
	}
}

// githubRelease is the part of GitHub's release object this package reads.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// fetchLatest returns the newest stable release, or nil when the repository
// hasn't published one yet.
func (c *Checker) fetchLatest(ctx context.Context) (*Release, error) {
	url := strings.TrimRight(c.APIBase, "/") + "/repos/" + c.Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "GoEat/"+c.Status().Current)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// /releases/latest 404s when nothing stable is published yet.
		return nil, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("GitHub rate limit reached; will try again later")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}

	var gr githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&gr); err != nil {
		return nil, fmt.Errorf("unexpected reply from GitHub: %w", err)
	}
	if gr.Draft || gr.Prerelease {
		return nil, nil
	}
	if _, ok := Parse(gr.TagName); !ok {
		return nil, fmt.Errorf("latest release tag %q isn't a version", gr.TagName)
	}

	return &Release{
		Version:     gr.TagName,
		Name:        gr.Name,
		URL:         c.releaseURL(gr),
		PublishedAt: gr.PublishedAt,
	}, nil
}

// releaseURL is the release page, rebuilt from the tag unless GitHub's link
// already points at this repository: it goes straight into an <a href>.
func (c *Checker) releaseURL(gr githubRelease) string {
	prefix := "https://github.com/" + c.Repo + "/releases/"
	if strings.HasPrefix(gr.HTMLURL, prefix) {
		return gr.HTMLURL
	}
	return prefix + "tag/" + gr.TagName
}
