package recipes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goeat/llm"
)

type replyGenerator struct {
	reply string
	req   llm.GenerateRequest
}

func (g *replyGenerator) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.req = req
	return llm.GenerateResponse{Content: g.reply}, nil
}
func (g *replyGenerator) ProviderName() string { return "fake" }
func (g *replyGenerator) ModelName() string    { return "fake" }

func TestStructureVideoRecipe(t *testing.T) {
	gen := &replyGenerator{reply: "```json\n" + `{
		"found": true,
		"title": "Chicken-Zucchini Meatballs With Feta",
		"servings": 4, "prep_minutes": 15, "cook_minutes": 25,
		"tags": ["#Dinner", "chicken"],
		"ingredients": [
			{"quantity": "1", "unit": "lb", "name": "ground chicken", "estimated": true},
			{"quantity": "", "unit": "", "name": "salt", "estimated": false},
			{"quantity": "2", "unit": "", "name": "", "estimated": false}
		],
		"steps": ["Grate the zucchini.", "  ", "Roast the meatballs."]
	}` + "\n```"}

	r, err := StructureVideoRecipe(context.Background(), gen, VideoText{
		Platform: "TikTok", Caption: "meatballs!", Transcript: "these meatballs are half chicken",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Chicken-Zucchini Meatballs With Feta" || r.Servings != 4 {
		t.Errorf("title/servings = %q/%d", r.Title, r.Servings)
	}
	if len(r.Ingredients) != 2 {
		t.Fatalf("ingredients = %+v (the nameless one should be dropped)", r.Ingredients)
	}
	if got := r.Ingredients[0]; got.Raw != "1 lb ground chicken" || got.Name != "ground chicken" || got.Unit != "lb" {
		t.Errorf("first ingredient = %+v", got)
	}
	if len(r.Steps) != 2 {
		t.Errorf("steps = %q (the blank one should be dropped)", r.Steps)
	}
	if strings.Join(r.Tags, ",") != "dinner,chicken,"+EstimatedTag {
		t.Errorf("tags = %q", r.Tags)
	}
	if !strings.Contains(gen.req.Prompt, "<transcript>\nthese meatballs are half chicken\n</transcript>") {
		t.Errorf("prompt is missing the transcript:\n%s", gen.req.Prompt)
	}
	if !strings.Contains(gen.req.Prompt, "<creator_comments>\n(none)\n</creator_comments>") {
		t.Errorf("prompt should mark missing comments as (none):\n%s", gen.req.Prompt)
	}
}

func TestStructureVideoRecipeNotFound(t *testing.T) {
	gen := &replyGenerator{reply: `{"found": false, "reason": "The recipe is at the link in bio."}`}
	_, err := StructureVideoRecipe(context.Background(), gen, VideoText{Caption: "link in bio"}, nil)
	if !errors.Is(err, ErrNoRecipeInVideo) || !strings.Contains(err.Error(), "link in bio") {
		t.Fatalf("err = %v", err)
	}
}

func TestStructureVideoRecipeBadJSON(t *testing.T) {
	gen := &replyGenerator{reply: "Sure! Here is the recipe: pasta"}
	if _, err := StructureVideoRecipe(context.Background(), gen, VideoText{Caption: "x"}, nil); err == nil {
		t.Fatal("expected an error for a non-JSON reply")
	}
}

func TestStructureVideoRecipeLenientShapes(t *testing.T) {
	gen := &replyGenerator{reply: `{
		"found": true, "title": "Meatballs", "servings": "4 servings", "prep_minutes": 10.0, "cook_minutes": null,
		"ingredients": [{"quantity": 1, "unit": "lb", "name": "ground chicken"}, {"quantity": "2", "unit": "", "name": "cups feta"}],
		"steps": [{"step": 1, "text": "Grate the zucchini."}, {"step": 2, "instruction": "Roast."}, "Serve."]
	}`}
	r, err := StructureVideoRecipe(context.Background(), gen, VideoText{Caption: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Servings != 4 || r.PrepMinutes != 10 || r.CookMinutes != 0 {
		t.Errorf("servings/prep/cook = %d/%d/%d", r.Servings, r.PrepMinutes, r.CookMinutes)
	}
	if r.Ingredients[0].Raw != "1 lb ground chicken" {
		t.Errorf("ingredient = %q", r.Ingredients[0].Raw)
	}
	if got := r.Ingredients[1]; got.Unit != "cups" || got.Name != "feta" {
		t.Errorf("unit left in the name: %+v", got)
	}
	if strings.Join(r.Steps, "|") != "Grate the zucchini.|Roast.|Serve." {
		t.Errorf("steps = %q", r.Steps)
	}
}
