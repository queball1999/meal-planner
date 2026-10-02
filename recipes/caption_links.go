package recipes

import (
	"net/url"
	"regexp"
	"strings"

	"goeat/video"
)

// LinksError wraps an import failure with the recipe-page links found in the
// video's caption and creator comments, so the progress page can offer to
// import one of those instead ("full recipe: example.com/…").
type LinksError struct {
	Err   error
	Links []string
}

func (e *LinksError) Error() string { return e.Err.Error() }
func (e *LinksError) Unwrap() error { return e.Err }

// maxCaptionLinks caps how many links are offered.
const maxCaptionLinks = 3

// captionURLRE finds links in free text: with a scheme, or bare
// ("nytcooking.com/recipes/123") as long as there is a path - a bare domain
// alone is more often a brand mention than a recipe.
var captionURLRE = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'()\[\]{}]+|\b(?:[a-z0-9-]+\.)+[a-z]{2,}/[^\s<>"'()\[\]{}]+`)

// notRecipeHosts are sites a caption links to that are never a recipe page:
// other social profiles, link-in-bio pages, shops and short links.
var notRecipeHosts = []string{
	"tiktok.com", "instagram.com", "youtube.com", "youtu.be", "facebook.com", "fb.com", "fb.me",
	"twitter.com", "x.com", "threads.net", "pinterest.com", "pin.it", "snapchat.com",
	"linktr.ee", "beacons.ai", "stan.store", "liketk.it", "shopltk.com", "shopmy.us", "bio.link",
	"amazon.com", "amzn.to", "a.co", "bit.ly", "spotify.com", "patreon.com",
}

// CaptionLinks returns the links in a video's caption and creator comments
// that could be a recipe page, in the order they appear, de-duplicated.
func CaptionLinks(t VideoText) []string {
	text := t.Caption + "\n" + strings.Join(t.Comments, "\n")
	var out []string
	seen := map[string]bool{}
	for _, m := range captionURLRE.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;:!?…")
		if !strings.Contains(strings.ToLower(m), "://") {
			m = "https://" + m
		}
		u, err := url.Parse(m)
		if err != nil || u.Hostname() == "" || video.IsVideoURL(m) || isNotRecipeHost(u.Hostname()) {
			continue
		}
		key := SourceKey(m)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
		if len(out) == maxCaptionLinks {
			break
		}
	}
	return out
}

func isNotRecipeHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range notRecipeHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}
