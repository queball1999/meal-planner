package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"goeat/db"
	"goeat/llm"
	"goeat/pricing"
)

// Consolidation is one set of catalog items found to be the same thing to buy
// and made into one.
type Consolidation struct {
	Kept   *db.Item
	Merged []string // names of the items merged into Kept
	// Skipped are names judged the same as Kept but left alone, because they
	// are not placeholders - see ConsolidatePlanItems.
	Skipped []string
}

// ConsolidatePlanItems finds the items on a plan's shopping list that are one
// grocery item under several names ("Black pepper", "pepper", "salt and
// pepper") and makes each set one item: the others are merged into it
// (db.MergeItems, which also records their names as aliases so the next plan
// does not bring them back), and the meals on plans ending on or after
// fromDate and the household's saved recipes are renamed to match.
//
// Which names mean the same thing is asked of the model, not scored: token
// overlap cannot tell "pepper" is black pepper and not bell pepper, which is
// why EnsureItem's own auto-link threshold never joins them.
//
// Only an auto-created placeholder is ever merged away, the same rule the
// shopping list's match dialog follows: an item the household made, or a
// builtin one, is not destroyed on a model's say-so. So a real item in a set
// is the one kept, and a second real item in the same set comes back in
// Skipped.
//
// The shopping list itself is not rebuilt here; the caller reprices.
func ConsolidatePlanItems(ctx context.Context, store db.Store, gen llm.Generator, householdID, planID int64, fromDate string) ([]Consolidation, error) {
	if gen == nil {
		return nil, nil
	}
	if err := LinkPlanIngredients(ctx, store, householdID, planID); err != nil {
		return nil, err
	}
	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	var items []*db.Item
	seen := map[int64]bool{}
	for _, ing := range ings {
		if ing.ItemID == nil || seen[*ing.ItemID] {
			continue
		}
		seen[*ing.ItemID] = true
		it, err := store.GetItem(ctx, *ing.ItemID)
		if err != nil {
			return nil, err
		}
		if it != nil && it.HouseholdID == householdID {
			items = append(items, it)
		}
	}
	if len(items) < 2 {
		return nil, nil
	}

	groups, err := findDuplicateItems(ctx, gen, items)
	if err != nil {
		return nil, err
	}

	var out []Consolidation
	for _, g := range groups {
		// A real catalog item in the set is the one kept, whichever the model
		// picked: only placeholders can be merged away, so keeping a
		// placeholder ("pepper") would leave the real item ("Black pepper")
		// standing beside it as a second line.
		if g[0].Source == "auto" {
			for i, it := range g {
				if it.Source != "auto" {
					g[0], g[i] = g[i], g[0]
					break
				}
			}
		}
		c := Consolidation{Kept: g[0]}
		// Every term that used to mean a merged item, for finding it in the
		// saved recipes, which carry names and no item id.
		oldTerms := map[string]bool{}
		for _, from := range g[1:] {
			if from.Source != "auto" {
				c.Skipped = append(c.Skipped, from.Name)
				continue
			}
			aliases, _ := store.ListItemAliases(ctx, householdID, from.ID)
			if err := store.MergeItems(ctx, householdID, from.ID, c.Kept.ID); err != nil {
				log.Printf("catalog: consolidate %q into %q: %v", from.Name, c.Kept.Name, err)
				continue
			}
			c.Merged = append(c.Merged, from.Name)
			oldTerms[from.NormalizedTerm] = true
			for _, a := range aliases {
				oldTerms[a.Alias] = true
			}
		}
		if len(c.Merged) > 0 {
			if err := store.RenameIngredientsForItem(ctx, c.Kept.ID, c.Kept.Name, c.Kept.NormalizedTerm, fromDate); err != nil {
				log.Printf("catalog: consolidate: rename ingredients to %q: %v", c.Kept.Name, err)
			}
			renameRecipeIngredients(ctx, store, householdID, oldTerms, c.Kept.Name)
		}
		if len(c.Merged) > 0 || len(c.Skipped) > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// renameRecipeIngredients renames every saved-recipe ingredient whose name
// normalizes to one of oldTerms. Failures are logged, not returned: the merge
// has already happened, and a recipe line left under its old name still
// resolves to the right item through the alias.
func renameRecipeIngredients(ctx context.Context, store db.Store, householdID int64, oldTerms map[string]bool, name string) {
	recipes, err := store.ListCatalogRecipes(ctx, householdID)
	if err != nil {
		log.Printf("catalog: consolidate: list recipes: %v", err)
		return
	}
	for _, rc := range recipes {
		ings, err := store.ListCatalogRecipeIngredients(ctx, rc.ID)
		if err != nil {
			log.Printf("catalog: consolidate: ingredients of recipe %d: %v", rc.ID, err)
			continue
		}
		for _, ing := range ings {
			if ing.Name == name || !oldTerms[pricing.Normalize(ing.Name)] {
				continue
			}
			if err := store.RenameCatalogRecipeIngredient(ctx, ing.ID, name); err != nil {
				log.Printf("catalog: consolidate: rename recipe ingredient %d: %v", ing.ID, err)
			}
		}
	}
}

// findDuplicateItems asks the model which of items are the same thing to buy.
// Each returned group is the item to keep followed by the ones to merge into
// it. The reply is matched back by position ("id"), and anything that does not
// add up - an id out of range, an item named in two groups - is dropped rather
// than guessed at.
func findDuplicateItems(ctx context.Context, gen llm.Generator, items []*db.Item) ([][]*db.Item, error) {
	type reqItem struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	req := make([]reqItem, len(items))
	for i, it := range items {
		req[i] = reqItem{ID: i, Name: it.Name}
	}
	itemsJSON, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	prompt := fmt.Sprintf(`These are the items on one household's shopping list for a week. Find the ones that are the same thing to buy under different names, so that each can be a single line.
Items: %s
Rules:
- Group only names a shopper would pick up the same product for: "pepper", "ground black pepper" and "black pepper" are one item.
- A seasoning pair such as "salt and pepper" goes with black pepper.
- Different foods stay separate however alike their names are: bell pepper is not black pepper, chicken thighs are not chicken breasts, canned tomatoes are not fresh tomatoes, brown rice is not white rice, olive oil is not vegetable oil.
- If you are not sure two names are the same product, leave them out.
- "keep" is the id in the group with the clearest, most specific name.
Return ONLY valid JSON - no prose, no markdown - as an array, [] when nothing is duplicated:
[{"keep": <id>, "merge": [<id>, ...]}]`, string(itemsJSON))

	resp, err := gen.Generate(llm.WithPurpose(ctx, "item_consolidate"), llm.GenerateRequest{
		System:            "You tidy a household's grocery list. Return only JSON.",
		Prompt:            prompt,
		MaxTokens:         16*len(items) + 512,
		SuppressReasoning: true,
	})
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	// From the first '[' to the last ']': models wrap "JSON only" in a code
	// fence or a sentence often enough.
	raw := resp.Content
	i, j := strings.Index(raw, "["), strings.LastIndex(raw, "]")
	if i < 0 || j < i {
		return nil, fmt.Errorf("bad response %q", raw)
	}
	var reply []struct {
		Keep  int   `json:"keep"`
		Merge []int `json:"merge"`
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), &reply); err != nil {
		return nil, fmt.Errorf("bad response %q: %w", raw, err)
	}

	used := map[int]bool{}
	valid := func(id int) bool { return id >= 0 && id < len(items) && !used[id] }
	var groups [][]*db.Item
	for _, g := range reply {
		if !valid(g.Keep) {
			continue
		}
		used[g.Keep] = true
		group := []*db.Item{items[g.Keep]}
		for _, id := range g.Merge {
			if !valid(id) {
				continue
			}
			used[id] = true
			group = append(group, items[id])
		}
		if len(group) > 1 {
			groups = append(groups, group)
		}
	}
	return groups, nil
}
