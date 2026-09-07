package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// MealDescriptionParse is the structured output of ParseMealDescription (§4.1).
type MealDescriptionParse struct {
	CandidateFoods []string          `json:"candidate_foods"`
	Staples        []string          `json:"staples"`
	Frequency      map[string]string `json:"frequency"` // food → "often"|"sometimes"|"occasionally"
}

const freetextSystem = `You are a meal-planning assistant. The user will describe what they typically eat for one meal of the day. Extract structured information as JSON with exactly these fields:
- candidate_foods: array of specific foods or dishes mentioned or clearly implied
- staples: array of staple pantry ingredients the described meals would require (e.g. milk for cereal, butter for toast)
- frequency: object mapping each candidate food to its implied frequency ("often", "sometimes", or "occasionally")

Return ONLY valid JSON. No explanation, no markdown fences.`

// ParseMealDescription calls the configured LLM to parse a free-text meal
// description into structured hints (§4.1). Returns the JSON string to store
// in meal_slot_hints.parsed_json.
//
// On empty input it returns ("null", nil) without an LLM call.
func ParseMealDescription(ctx context.Context, gen Generator, slot, text string) (string, int, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "null", 0, 0, nil
	}

	prompt := fmt.Sprintf("Meal slot: %s\n\nDescription: %s", slot, text)
	resp, err := gen.Generate(ctx, GenerateRequest{
		System:            freetextSystem,
		Prompt:            prompt,
		MaxTokens:         1024,
		SuppressReasoning: true,
	})
	if err != nil {
		return "null", 0, 0, fmt.Errorf("parse meal description: %w", err)
	}

	// Validate the response is parseable JSON with the expected fields.
	raw := strings.TrimSpace(resp.Content)
	var parsed MealDescriptionParse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Printf("llm: parse meal description: invalid JSON from %s: %v", gen.ProviderName(), err)
		return "null", resp.InputTokens, resp.OutputTokens, nil
	}

	return raw, resp.InputTokens, resp.OutputTokens, nil
}
