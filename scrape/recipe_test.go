package scrape

import "testing"

// allrecipes.com (and many WordPress recipe plugins) ship the Recipe node
// inside a top-level JSON-LD array. The old parser unmarshalled each block
// into map[string]any, which failed on the array and left only the OpenGraph
// title/image - the "image but no ingredients" import bug.
const arrayWrappedLD = `<html><head>
<meta property="og:title" content="Easy Indian Butter Chicken">
<meta property="og:image" content="https://img.example/butter.jpg">
<script type="application/ld+json">
[
  {"@type":"BreadcrumbList","itemListElement":[]},
  {
    "@type":["Recipe"],
    "name":"Easy Indian Butter Chicken",
    "image":{"@type":"ImageObject","url":"https://img.example/butter.jpg"},
    "recipeYield":4,
    "prepTime":"PT15M",
    "cookTime":"PT30M",
    "recipeIngredient":["2 tablespoons butter","1 pound chicken thighs, cubed","1 (15 ounce) can tomato sauce"],
    "recipeInstructions":[
      {"@type":"HowToSection","itemListElement":[
        {"@type":"HowToStep","text":"Melt <b>butter</b> in a skillet over medium heat."},
        {"@type":"HowToStep","text":"Add chicken &amp; cook until browned."}
      ]},
      {"@type":"HowToStep","text":"Stir in tomato sauce and simmer 20 minutes."}
    ],
    "recipeCategory":"Dinner"
  }
]
</script></head><body></body></html>`

func TestParseRecipeArrayWrappedJSONLD(t *testing.T) {
	r, err := ParseRecipe(arrayWrappedLD, "https://www.allrecipes.com/recipe/1234/butter-chicken/")
	if err != nil {
		t.Fatalf("ParseRecipe: %v", err)
	}
	if r.Title != "Easy Indian Butter Chicken" {
		t.Errorf("Title = %q", r.Title)
	}
	if len(r.Ingredients) != 3 {
		t.Fatalf("Ingredients = %d, want 3: %#v", len(r.Ingredients), r.Ingredients)
	}
	if r.Ingredients[0].Raw != "2 tablespoons butter" {
		t.Errorf("Ingredients[0] = %q", r.Ingredients[0].Raw)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("Steps = %d, want 3: %#v", len(r.Steps), r.Steps)
	}
	if r.Steps[0] != "Melt butter in a skillet over medium heat." {
		t.Errorf("Steps[0] = %q (tags should be stripped)", r.Steps[0])
	}
	if r.Steps[1] != "Add chicken & cook until browned." {
		t.Errorf("Steps[1] = %q (entities should be unescaped)", r.Steps[1])
	}
	if r.Servings != 4 {
		t.Errorf("Servings = %d, want 4 (numeric recipeYield)", r.Servings)
	}
	if r.PrepMinutes != 15 || r.CookMinutes != 30 {
		t.Errorf("Prep/Cook = %d/%d, want 15/30", r.PrepMinutes, r.CookMinutes)
	}
	if r.ImageURL == "" {
		t.Error("ImageURL empty")
	}
}

// A @graph-wrapped document with the Recipe nested a level down.
const graphWrappedLD = `<script type="application/ld+json">
{"@context":"https://schema.org","@graph":[
 {"@type":"WebPage","name":"page"},
 {"@type":"Recipe","name":"Graph Soup",
  "recipeIngredient":["1 cup water"],
  "recipeInstructions":"Boil water.\nServe hot."}
]}</script>`

func TestParseRecipeGraphWrapped(t *testing.T) {
	r, err := ParseRecipe(graphWrappedLD, "https://example.com/soup")
	if err != nil {
		t.Fatalf("ParseRecipe: %v", err)
	}
	if r.Title != "Graph Soup" {
		t.Errorf("Title = %q", r.Title)
	}
	if len(r.Ingredients) != 1 {
		t.Errorf("Ingredients = %#v", r.Ingredients)
	}
	if len(r.Steps) != 2 {
		t.Errorf("Steps = %#v, want 2 lines", r.Steps)
	}
}

// Entities - including double-encoded ones - must be decoded on import, or
// "Steve&#39;s" shows literally on the saved recipe.
func TestParseRecipeDecodesEntities(t *testing.T) {
	page := `<script type="application/ld+json">{"@type":"Recipe",
		"name":"Steve&#39;s Chili",
		"keywords":"Tex-Mex &amp; Southwest",
		"recipeIngredient":["1 cup Frank&amp;#39;s hot sauce"],
		"recipeInstructions":["Stir &amp; serve"]}</script>`
	r, err := ParseRecipe(page, "https://example.com/r")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Steve's Chili" {
		t.Errorf("title = %q", r.Title)
	}
	if len(r.Tags) != 1 || r.Tags[0] != "Tex-Mex & Southwest" {
		t.Errorf("tags = %q", r.Tags)
	}
	if len(r.Ingredients) != 1 || r.Ingredients[0].Raw != "1 cup Frank's hot sauce" {
		t.Errorf("ingredients = %+v", r.Ingredients)
	}
	if len(r.Steps) != 1 || r.Steps[0] != "Stir & serve" {
		t.Errorf("steps = %q", r.Steps)
	}
}
