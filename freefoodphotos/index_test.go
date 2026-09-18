package freefoodphotos

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakePage renders one freefoodphotos.com category page's relevant markup
// (just the <a><img alt="..."></a> entries BuildIndex's regex looks for).
func fakePage(entries ...[2]string) string {
	var b strings.Builder
	b.WriteString("<html><body><div class=\"gallery\">")
	for _, e := range entries {
		file, caption := e[0], e[1]
		fmt.Fprintf(&b, `<a href="slides/%s.html"><img src="thumbs/%s.jpg" alt="%s"></a>`, file, file, caption)
	}
	b.WriteString("</div></body></html>")
	return b.String()
}

func fakeFetcher(pages map[string]string) PageFetcher {
	return func(_ context.Context, url string) (string, error) {
		for slug, html := range pages {
			if strings.Contains(url, "/"+slug+"/") {
				return html, nil
			}
		}
		return "", fmt.Errorf("no fake page for %s", url)
	}
}

func TestBuildIndexAndMatch(t *testing.T) {
	pages := map[string]string{
		"fruit": fakePage(
			[2]string{"apple1", "Fresh red apple isolated on white background"},
			[2]string{"bbq1", "Assorted meat grilling over a barbecue"}, // composed - should be dropped
		),
		"meat": fakePage(
			[2]string{"steak1", "Raw ribeye beef steak"},
		),
	}
	idx, err := BuildIndex(context.Background(), fakeFetcher(pages), Options{
		Slugs: []string{"fruit", "meat"},
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// The composed BBQ caption must be filtered out by looksComposed.
	if got, want := idx.Len(), 2; got != want {
		t.Fatalf("Len() = %d, want %d (composed caption should be dropped)", got, want)
	}

	p, ok := idx.Match("Apple")
	if !ok {
		t.Fatal("Match(\"Apple\") found nothing")
	}
	if !strings.Contains(p.ImageURL, "apple1") {
		t.Errorf("matched %+v, want the apple photo", p)
	}
	if p.Category != "Produce" {
		t.Errorf("category = %q, want Produce", p.Category)
	}
	if !idx.IsUsed(p.ImageURL) {
		t.Error("a matched photo should be marked used")
	}

	// A second, unrelated item must not be handed the already-used apple photo.
	if _, ok := idx.Match("Apple pie"); ok {
		// Fine either way in principle, but with only one apple photo and it
		// already used, this must not re-match the same photo to a second item.
		t.Error("Match(\"Apple pie\") should not reuse the apple photo already assigned")
	}

	// Ribeye steak should match the steak caption via shared significant words.
	if p, ok := idx.Match("Ribeye steak"); !ok || !strings.Contains(p.ImageURL, "steak1") {
		t.Errorf("Match(\"Ribeye steak\") = %+v, %v, want the steak photo", p, ok)
	}

	// A name sharing no significant words with anything should not match.
	if _, ok := idx.Match("Zucchini noodles"); ok {
		t.Error("Match(\"Zucchini noodles\") should find nothing")
	}
}

func TestBuildIndexPropagatesFetchError(t *testing.T) {
	_, err := BuildIndex(context.Background(), fakeFetcher(map[string]string{}), Options{
		Slugs: []string{"fruit"},
	})
	if err == nil {
		t.Fatal("expected an error when the fetcher has no page for the requested slug")
	}
}

func TestBuildIndexRespectsLimitPerCategory(t *testing.T) {
	pages := map[string]string{
		"fruit": fakePage(
			[2]string{"apple1", "Fresh red apple"},
			[2]string{"banana1", "Ripe yellow banana"},
			[2]string{"pear1", "Green pear fruit"},
		),
	}
	idx, err := BuildIndex(context.Background(), fakeFetcher(pages), Options{
		Slugs: []string{"fruit"}, LimitPerCategory: 2,
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if got, want := idx.Len(), 2; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}
