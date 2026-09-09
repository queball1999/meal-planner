package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goeat/catalog"
	"goeat/db"
)

// PriceChecker looks up a real price for a plain grocery term, so the
// generation tool loop (below) can ground its choices in what things
// actually cost instead of guessing - which is what let a plan quietly come
// in far over budget with every ingredient looking individually reasonable.
// Returns ok=false when nothing is known about the term (no stores
// configured, no pricing chain, or the provider chain found nothing).
type PriceChecker func(ctx context.Context, term string) (priceCents int64, unit string, storeName string, ok bool)

// genToolCtx is what a generation tool needs to answer a call - the
// household-scoped read access the chat assistant's equivalent tools use
// (agent.Session), duplicated here rather than imported because agent
// already imports plan (fill_slot, move_meal, ...) and Go does not allow the
// reverse.
type genToolCtx struct {
	store       db.Store
	householdID int64
	checker     PriceChecker // nil when no pricing chain is configured
}

// genToolResult is one tool call's outcome: a short line for the transcript
// and the structured data (if any) the model needs to keep reasoning.
type genToolResult struct {
	Summary string
	Data    any
}

// genToolDescriptions is appended to the tool-loop system prompt - see
// toolProtocolPreamble in prompt.go. Written by hand for the same reason
// agent.Registry.Describe() is: a short readable list costs a fraction of
// the tokens a JSON Schema dump would and models follow it more reliably.
const genToolDescriptions = `- read_pantry(): Read what the household already has on hand, so you can lean on it instead of buying more.
- search_recipes(query?): Search the household's saved recipes by title or tag. Reuse a good match by giving that meal the same title, ingredients, and steps instead of inventing a near-duplicate.
- search_items(query): Search the household's grocery item catalog for a canonical name.
- check_price(ingredient): Look up a real price for one ingredient, e.g. {"ingredient": "chicken breast"}. Returns price_cents, unit, and store when known, or found:false when nothing is on record - that is not an error, just try a plainer name or move on.
`

// runGenTool dispatches one tool call by name. A fixed switch rather than the
// chat assistant's generic Registry: the set of tools available during
// generation is small and unlikely to grow at the same pace as the chat
// tools, so the extra abstraction would not be earning its keep here.
func runGenTool(ctx context.Context, gc *genToolCtx, name string, args json.RawMessage) (genToolResult, error) {
	switch name {
	case "read_pantry":
		return genReadPantry(ctx, gc)
	case "search_recipes":
		return genSearchRecipes(ctx, gc, args)
	case "search_items":
		return genSearchItems(ctx, gc, args)
	case "check_price":
		return genCheckPrice(ctx, gc, args)
	default:
		return genToolResult{}, fmt.Errorf("no tool named %q", name)
	}
}

func genReadPantry(ctx context.Context, gc *genToolCtx) (genToolResult, error) {
	rows, err := gc.store.ListPantryItems(ctx, gc.householdID)
	if err != nil {
		return genToolResult{}, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"name": r.Name, "quantity": r.QuantityOnHand, "unit": r.Unit,
		})
	}
	return genToolResult{
		Summary: fmt.Sprintf("Read the pantry: %d items on hand.", len(out)),
		Data:    map[string]any{"items": out},
	}, nil
}

func genSearchRecipes(ctx context.Context, gc *genToolCtx, args json.RawMessage) (genToolResult, error) {
	var a struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(args, &a)

	var (
		recipes []*db.CatalogRecipe
		err     error
	)
	if q := strings.TrimSpace(a.Query); q != "" {
		recipes, err = gc.store.SearchCatalogRecipes(ctx, gc.householdID, q)
	} else {
		recipes, err = gc.store.ListCatalogRecipes(ctx, gc.householdID)
	}
	if err != nil {
		return genToolResult{}, err
	}

	// Capped for the same reason agent.tools_shopping's search_recipes caps:
	// a household with hundreds of saved recipes should not spend the whole
	// context window on a list the model will not read in full.
	const max = 25
	out := make([]map[string]any, 0, max)
	for i, rc := range recipes {
		if i >= max {
			break
		}
		out = append(out, map[string]any{
			"title": rc.Title, "servings": rc.Servings,
			"minutes": rc.PrepMinutes + rc.CookMinutes, "tags": rc.Tags,
		})
	}
	return genToolResult{
		Summary: fmt.Sprintf("Found %d saved recipes.", len(out)),
		Data:    map[string]any{"recipes": out, "truncated": len(recipes) > max},
	}, nil
}

func genSearchItems(ctx context.Context, gc *genToolCtx, args json.RawMessage) (genToolResult, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return genToolResult{}, err
	}
	items, err := gc.store.ListItems(ctx, gc.householdID)
	if err != nil {
		return genToolResult{}, err
	}
	matches := catalog.SuggestItems(a.Query, items, 10)
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		out = append(out, map[string]any{"name": m.Name})
	}
	return genToolResult{
		Summary: fmt.Sprintf("Found %d catalog items matching %q.", len(out), a.Query),
		Data:    map[string]any{"items": out},
	}, nil
}

func genCheckPrice(ctx context.Context, gc *genToolCtx, args json.RawMessage) (genToolResult, error) {
	var a struct {
		Ingredient string `json:"ingredient"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return genToolResult{}, err
	}
	term := strings.TrimSpace(a.Ingredient)
	if term == "" {
		return genToolResult{}, fmt.Errorf("which ingredient?")
	}
	if gc.checker == nil {
		return genToolResult{
			Summary: "No pricing is configured - estimate this one yourself.",
			Data:    map[string]any{"found": false},
		}, nil
	}
	priceCents, unit, store, ok := gc.checker(ctx, term)
	if !ok {
		return genToolResult{
			Summary: fmt.Sprintf("No price on record for %q - estimate it.", term),
			Data:    map[string]any{"found": false},
		}, nil
	}
	return genToolResult{
		Summary: fmt.Sprintf("%s: $%.2f / %s at %s.", term, float64(priceCents)/100, unit, store),
		Data: map[string]any{
			"found": true, "price_cents": priceCents, "unit": unit, "store": store,
		},
	}, nil
}
