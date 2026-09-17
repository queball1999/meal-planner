package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goeat/llm"
)

// genMaxSteps caps how many tool calls one generation may make before it must
// produce the plan. Generous enough to check pantry, a couple of recipe
// searches, and a price on every ingredient in a substantial week (30-40
// lines) without letting a confused model loop indefinitely - each step is a
// full model round-trip, so this is also most of what bounds how long
// generation can take before this loop gives up and asks for the plan
// outright (see runGenerationLoop's final forced turn).
const genMaxSteps = 24

// toolProtocolPreamble is prepended to plan/prompt.go's systemPrompt for the
// tool-capable generation loop. Mirrors agent/loop.go's protocolPrompt in
// shape (same "tool" vs. final-answer JSON convention, for the same
// cross-provider portability reason - see that file's comment) but ends in
// the plan schema itself rather than a free-text "say", since generation has
// no user turn to reply to.
const toolProtocolPreamble = `Before writing the plan, you may look a few things up.

Reply with exactly ONE JSON object and nothing else - no prose outside it, no
markdown fences.

To call a tool:
  {"tool": "check_price", "args": {"ingredient": "chicken breast"}}

When you have everything you need, reply with the final plan instead - the
JSON object described below ("meals": [...]), not a tool call.

Rules:
- One tool per reply. You will be given the result and can then call another.
- You do not need to check every ingredient's price - a representative sample
  (proteins, specialty items, anything you are unsure of) is enough to keep
  the week's total realistic. Checking all of them is a waste of your own
  time budget.
- Reuse a good match from search_recipes rather than inventing a near-dupe.
- If a tool returns found:false or an error, that is not a reason to stop -
  estimate that one yourself and move on.
- You have a limited number of lookups. When you are close to using them up,
  stop looking things up and write the final plan with what you have.

Available tools:
` + genToolDescriptions + `
`

// genAction is one parsed model reply during the generation tool loop: either
// a tool call, or (when Tool is empty) the raw text is the final plan JSON.
type genAction struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// looksLikeToolCall reports whether raw parses as {"tool": "...", ...} with a
// non-empty tool name - the only shape that is NOT the final plan. Tried
// before assuming raw is the plan JSON itself, since a stray tool call must
// never be handed to json.Unmarshal(&GeneratedPlan{}) and silently produce a
// zero-meal plan.
func looksLikeToolCall(raw string) (genAction, bool) {
	s := stripFence(strings.TrimSpace(raw))
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return genAction{}, false
	}
	var a genAction
	if err := json.Unmarshal([]byte(s[start:end+1]), &a); err != nil {
		return genAction{}, false
	}
	if a.Tool == "" {
		return genAction{}, false
	}
	if len(a.Args) == 0 {
		a.Args = json.RawMessage("{}")
	}
	return a, true
}

// stripFence removes a ```-fenced wrapper a model sometimes puts around its
// reply. Same tolerance agent/loop.go's stripFence applies, duplicated here
// for the same package-cycle reason as genToolCtx (see tools.go).
func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// runGenerationLoop drives the model through the tool loop and returns the
// raw text of its final reply - the caller (generate, in generate.go) parses
// that exactly as it always parsed a single-shot response (stripFences +
// json.Unmarshal into GeneratedPlan), so Validate/ClampQuantities/persistPlan
// downstream are unaffected by how many round-trips it took to get there.
func runGenerationLoop(ctx context.Context, gen llm.Generator, systemPrompt, userPrompt string, gc *genToolCtx, j *Job) (string, error) {
	system := toolProtocolPreamble + systemPrompt

	var transcript strings.Builder
	transcript.WriteString(userPrompt)

	for step := 0; step < genMaxSteps; step++ {
		// The last couple of steps: stop offering the choice and require the
		// plan itself, so a model that spent its whole budget looking things
		// up still hands back something rather than nothing.
		prompt := transcript.String()
		if step >= genMaxSteps-2 {
			prompt += "\n\nYou are nearly out of lookups. Reply with the final plan now."
		}

		j.EmitLLMStart()
		resp, err := gen.Generate(ctx, llm.GenerateRequest{
			System:    system,
			Prompt:    prompt,
			MaxTokens: planGenMaxTokens,
			OnDelta:   j.EmitDelta,
		})
		if err != nil {
			return "", fmt.Errorf("llm generate: %w", err)
		}

		action, isCall := looksLikeToolCall(resp.Content)
		if !isCall {
			return resp.Content, nil
		}

		if step == 0 {
			j.EmitStatus("Checking pantry, recipes, and prices before drafting your week…")
		}

		res, terr := runGenTool(ctx, gc, action.Tool, action.Args)
		if terr != nil {
			transcript.WriteString(fmt.Sprintf("\nTOOL %s ERROR: %s\n", action.Tool, terr.Error()))
			continue
		}
		transcript.WriteString(fmt.Sprintf("\nTOOL %s OK: %s\n", action.Tool, res.Summary))
		if res.Data != nil {
			if blob, mErr := json.Marshal(res.Data); mErr == nil {
				transcript.WriteString("DATA: ")
				transcript.Write(blob)
				transcript.WriteString("\n")
			}
		}
	}

	return "", fmt.Errorf("generation used its whole tool-call budget (%d) without producing a plan", genMaxSteps)
}
