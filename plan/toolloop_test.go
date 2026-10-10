package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goeat/db"
	"goeat/llm"
)

// scriptedGenerator replays a fixed sequence of raw replies, one per call -
// a stand-in for a model working through a tool-calling conversation one
// round-trip at a time.
type scriptedGenerator struct {
	replies []string
	calls   int
	prompts []string // every prompt seen, for assertions on what the model was told
}

func (g *scriptedGenerator) ProviderName() string { return "fake" }
func (g *scriptedGenerator) ModelName() string    { return "fake-model" }

func (g *scriptedGenerator) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.prompts = append(g.prompts, req.Prompt)
	if g.calls >= len(g.replies) {
		g.calls++
		return llm.GenerateResponse{Content: g.replies[len(g.replies)-1]}, nil
	}
	reply := g.replies[g.calls]
	g.calls++
	return llm.GenerateResponse{Content: reply}, nil
}

func onePlan(titlePrefix string) string {
	var meals []GeneratedMeal
	for _, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			meals = append(meals, GeneratedMeal{
				Day: day, Slot: slot, Title: titlePrefix + " " + day + " " + slot,
				Effort: "quick", Servings: 2, CookedPortions: 2,
				Ingredients: []GeneratedIngredient{{Name: "canned black beans", Quantity: 1, Unit: "can", EstPriceCents: 129}},
				Steps:       []string{"Cook it."},
			})
		}
	}
	raw, _ := json.Marshal(GeneratedPlan{Meals: meals})
	return string(raw)
}

// The model can look things up before committing to a plan - each tool call
// is a separate round-trip, and the loop must feed the tool's result back
// rather than treating the tool call itself as the final answer.
func TestGenerate_ToolLoopCallsToolsBeforeFinalPlan(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	if _, err := store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID: hhID, Name: "Rice", NormalizedTerm: "rice", QuantityOnHand: 2, Unit: "lb",
	}); err != nil {
		t.Fatalf("seed pantry: %v", err)
	}

	gen := &scriptedGenerator{replies: []string{
		`{"tool": "read_pantry", "args": {}}`,
		`{"tool": "search_recipes", "args": {"query": "chili"}}`,
		onePlan("Tool"),
	}}

	planID, err := generateTestWeek(ctx, store, gen, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if gen.calls != 3 {
		t.Fatalf("model was called %d times, want 3 (two tool calls then the plan)", gen.calls)
	}

	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil || len(meals) == 0 {
		t.Fatalf("list meals: %v", err)
	}
	if !strings.HasPrefix(meals[0].Title, "Tool ") {
		t.Errorf("meal title = %q, want the final scripted plan's titles", meals[0].Title)
	}

	// The pantry read's result must have reached the model as real data, not
	// just a summary line - the whole point is being able to act on it.
	found := false
	for _, p := range gen.prompts {
		if strings.Contains(p, `"name":"Rice"`) || strings.Contains(p, `"name": "Rice"`) {
			found = true
		}
	}
	if !found {
		t.Error("the pantry contents never appeared in a later prompt")
	}
}

// check_price lets generation ground an ingredient in a real price instead of
// guessing - the fix for a plan that is over budget despite every meal
// looking individually reasonable.
func TestGenerate_ToolLoopChecksPrices(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	var checkedTerm string
	checker := PriceChecker(func(ctx context.Context, term string) (int64, string, string, bool) {
		checkedTerm = term
		return 899, "lb", "Kroger", true
	})

	gen := &scriptedGenerator{replies: []string{
		`{"tool": "check_price", "args": {"ingredient": "chicken breast"}}`,
		onePlan("Priced"),
	}}

	if _, err := generateTestWeek(ctx, store, gen, hhID, nil, checker, nil); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if checkedTerm != "chicken breast" {
		t.Errorf("checker called with %q, want %q", checkedTerm, "chicken breast")
	}

	found := false
	for _, p := range gen.prompts {
		if strings.Contains(p, "899") && strings.Contains(p, "Kroger") {
			found = true
		}
	}
	if !found {
		t.Error("the price lookup result never appeared in a later prompt")
	}
}

// No PriceChecker configured (no pricing chain) must not break generation -
// check_price degrades to "estimate it yourself" rather than erroring the
// whole run.
func TestGenerate_ToolLoopCheckPriceWithNilChecker(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	gen := &scriptedGenerator{replies: []string{
		`{"tool": "check_price", "args": {"ingredient": "saffron"}}`,
		onePlan("NoChecker"),
	}}

	if _, err := generateTestWeek(ctx, store, gen, hhID, nil, nil, nil); err != nil {
		t.Fatalf("generate: %v", err)
	}
}

// A model that never stops calling tools must not hang generation forever -
// it has to fail cleanly once the step budget is spent.
func TestGenerate_ToolLoopStepBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	gen := &scriptedGenerator{replies: []string{`{"tool": "read_pantry", "args": {}}`}} // repeats forever

	_, err := generateTestWeek(ctx, store, gen, hhID, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error once the tool-call budget ran out")
	}
	if gen.calls != genMaxSteps {
		t.Errorf("model was called %d times, want exactly the step budget (%d)", gen.calls, genMaxSteps)
	}
}

// An unknown tool name is a mistake the model can recover from - fed back as
// an error, not a fatal abort of the whole generation.
func TestGenerate_ToolLoopUnknownToolIsRecoverable(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	gen := &scriptedGenerator{replies: []string{
		`{"tool": "set_pantry_qty", "args": {"name": "rice", "quantity": 5}}`,
		onePlan("Recovered"),
	}}

	if _, err := generateTestWeek(ctx, store, gen, hhID, nil, nil, nil); err != nil {
		t.Fatalf("generate: %v", err)
	}

	sawError := false
	for _, p := range gen.prompts {
		if strings.Contains(p, "TOOL set_pantry_qty ERROR") {
			sawError = true
		}
	}
	if !sawError {
		t.Error("the unknown-tool error never reached a later prompt")
	}
}
