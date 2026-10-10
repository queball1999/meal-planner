package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goeat/db"
	"goeat/llm"
)

// DescribedRecipe is the saved recipe WriteRecipeFromRequest ended up with.
type DescribedRecipe struct {
	ID    int64
	Title string
	// Reused is true when the household already had a recipe by the title the
	// model chose: that one is returned as it stands.
	Reused bool
}

// WriteRecipeFromRequest has the model write one recipe for what the user
// asked for in a few words ("sloppy joes") and files it in the household's
// catalog - the plan page's "add a new recipe" when nothing saved fits a slot.
// It only writes the recipe; putting it in a slot is the caller's next step,
// by the same path as picking any saved recipe.
//
// The recipe is checked against the household's allergies and diet the way a
// regenerated meal is (validateSingleMeal), and refused rather than saved when
// it breaks them.
func WriteRecipeFromRequest(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, request string, servings int) (*DescribedRecipe, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return nil, errors.New("say what dish you want")
	}
	if gen == nil {
		return nil, errors.New("no AI provider is configured")
	}
	if servings < 1 {
		servings = 2
	}
	profile, err := Resolve(ctx, store, householdID)
	if err != nil {
		return nil, fmt.Errorf("resolve preferences: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Write one recipe for this request: %q\n", request)
	fmt.Fprintf(&b, "It serves %d.\n", servings)
	if len(profile.Allergies) > 0 {
		fmt.Fprintf(&b, "Never use these (allergies): %s.\n", strings.Join(profile.Allergies, ", "))
	}
	if len(profile.DietTags) > 0 {
		fmt.Fprintf(&b, "The household's diet: %s.\n", strings.Join(profile.DietTags, ", "))
	}
	b.WriteString(`Return ONLY valid JSON - no prose, no markdown:
{"title": "<the dish's plain name>", "effort": "<quick|standard|elaborate>", "servings": <integer>,
 "prep_minutes": <integer>, "cook_minutes": <integer>, "tags": ["<tag>", ...],
 "ingredients": [{"name": "<ingredient>", "quantity": <number>, "unit": "<g|oz|lb|ml|cup|tbsp|tsp|each>"}],
 "steps": ["<step>", ...]}
Every ingredient needs a quantity and a unit; use "each" for things counted whole.`)

	resp, err := gen.Generate(llm.WithPurpose(ctx, "recipe_request"), llm.GenerateRequest{
		System:            "You are a home cook writing a practical recipe. Return only JSON.",
		Prompt:            b.String(),
		MaxTokens:         2048,
		SuppressReasoning: true,
	})
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	// From the first '{' to the last '}': a code fence or a stray sentence
	// around "JSON only" is common.
	raw := resp.Content
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j < i {
		return nil, errors.New("the AI did not return a recipe - try wording it differently")
	}
	var gm GeneratedMeal
	if err := json.Unmarshal([]byte(raw[i:j+1]), &gm); err != nil {
		return nil, errors.New("the AI's recipe could not be read - try again")
	}

	one := []GeneratedMeal{gm}
	CleanMealTitles(GeneratedPlan{Meals: one})
	gm = one[0]
	gm.Title = strings.TrimSpace(gm.Title)
	switch {
	case gm.Title == "":
		return nil, errors.New("the AI's recipe had no title - try again")
	case db.IsLeftoverTitle(gm.Title):
		return nil, errors.New("leftovers are not a recipe - name the dish itself")
	case len(gm.Ingredients) == 0:
		return nil, errors.New("the AI's recipe had no ingredients - try again")
	}
	if gm.Servings < 1 {
		gm.Servings = servings
	}
	for k := range gm.Ingredients {
		// A countable thing with no unit ("2 buns") is the usual omission.
		if strings.TrimSpace(gm.Ingredients[k].Unit) == "" {
			gm.Ingredients[k].Unit = "each"
		}
	}
	if err := validateSingleMeal(gm, profile); err != nil {
		return nil, fmt.Errorf("that recipe does not fit this household: %w", err)
	}

	id, created, err := ensureGeneratedRecipe(ctx, store, householdID, gm)
	if err != nil {
		return nil, fmt.Errorf("save recipe: %w", err)
	}
	return &DescribedRecipe{ID: id, Title: gm.Title, Reused: !created}, nil
}
