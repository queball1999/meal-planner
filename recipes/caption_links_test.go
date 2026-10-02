package recipes

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCaptionLinks(t *testing.T) {
	text := VideoText{
		Caption: "Best ever chili 🌶️ Full recipe: https://www.nytcooking.com/recipes/123-chili. " +
			"Also on my blog (myblog.com/chili/) - shop my pans https://amzn.to/abc, linktr.ee/chef, " +
			"follow https://www.instagram.com/chef and watch https://youtu.be/xyz. e.g. salt.",
		Comments: []string{
			"Recipe here 👉 https://nytcooking.com/recipes/123-chili?utm_source=tiktok",
			"part 2: https://www.tiktok.com/@chef/video/99",
		},
	}
	want := []string{"https://www.nytcooking.com/recipes/123-chili", "https://myblog.com/chili/"}
	if got := CaptionLinks(text); !reflect.DeepEqual(got, want) {
		t.Errorf("CaptionLinks = %q, want %q", got, want)
	}
	if got := CaptionLinks(VideoText{Caption: "recipe at the link in bio! #dinner"}); len(got) != 0 {
		t.Errorf("CaptionLinks(no links) = %q, want none", got)
	}
}

func TestLinksErrorKeepsNoRecipeCause(t *testing.T) {
	gen := &replyGenerator{reply: `{"found": false, "reason": "the recipe is on the creator's blog"}`}
	_, err := StructureVideoRecipe(context.Background(), gen, VideoText{Caption: "x"}, nil)
	wrapped := error(&LinksError{Err: err, Links: []string{"https://example.com/r"}})
	if !errors.Is(wrapped, ErrNoRecipeInVideo) {
		t.Fatalf("LinksError(%v) should still match ErrNoRecipeInVideo", err)
	}
	if wrapped.Error() != err.Error() {
		t.Errorf("Error() = %q, want the wrapped %q", wrapped.Error(), err.Error())
	}
}
