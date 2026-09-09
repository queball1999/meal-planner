package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"goeat/llm"
)

// AIEstimateProvider asks the configured LLM for a typical current price (§6.2).
// Always the floor of the chain - available even with zero integrations.
// Results are tagged ConfidenceEstimate and persisted to price_cache by the chain.
type AIEstimateProvider struct {
	gen    llm.Generator
	region string // household ZIP/metro for locale context
}

func NewAIEstimateProvider(gen llm.Generator, region string) *AIEstimateProvider {
	return &AIEstimateProvider{gen: gen, region: region}
}

func (a *AIEstimateProvider) Name() string { return "ai_estimate" }

func (a *AIEstimateProvider) Lookup(ctx context.Context, term string, _ int64, region string) (*PriceResult, error) {
	if a.gen == nil {
		return nil, nil // no LLM configured
	}
	r := region
	if r == "" {
		r = a.region
	}

	prompt := fmt.Sprintf(
		`Estimate the typical US grocery store price for "%s" in the region/ZIP: %s.
Return ONLY valid JSON - no prose, no markdown:
{"price_cents": <integer cents>, "purchase_unit": "<each|oz|lb|cup|pack>", "pack_size": <float>}`,
		term, r,
	)

	// A one-line price lookup needs no chain of thought, and a reasoning model
	// given one will burn the entire budget on it and answer with nothing. 512
	// tokens is still ample for the ~30-token JSON object on backends that
	// don't support the switch.
	resp, err := a.gen.Generate(ctx, llm.GenerateRequest{
		System:            "You are a grocery pricing assistant. Return only JSON.",
		Prompt:            prompt,
		MaxTokens:         512,
		SuppressReasoning: true,
	})
	if err != nil {
		log.Printf("pricing: ai estimate for %q: generate: %v", term, err)
		return nil, nil // fail soft
	}

	var out struct {
		PriceCents   int64   `json:"price_cents"`
		PurchaseUnit string  `json:"purchase_unit"`
		PackSize     float64 `json:"pack_size"`
	}
	raw := stripCodeFences(strings.TrimSpace(resp.Content))
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("pricing: ai estimate for %q: bad response %q: %v", term, raw, err)
		return nil, nil // unparseable - fail soft
	}
	if out.PriceCents <= 0 {
		return nil, nil
	}
	if out.PackSize <= 0 {
		out.PackSize = 1
	}

	unit, packSize := sanitizeEstimatePack(out.PurchaseUnit, out.PackSize)

	return &PriceResult{
		PriceCents:   out.PriceCents,
		PurchaseUnit: unit,
		PackSize:     packSize,
		Source:       "estimate",
		Confidence:   ConfidenceEstimate,
		FetchedAt:    time.Now().UTC(),
	}, nil
}

// sanitizeEstimatePack fixes the single most damaging shape of a bad LLM price
// reply: a compound purchase_unit ("dozen") paired with a pack_size that is the
// unit's own expansion factor (12) rather than how many of that unit are in one
// pack (a carton is 1 dozen, an 18-count is 1.5). Left through, "dozen" + 12
// reconciles to 144 eggs a pack - one carton then reads as "144 eggs, $3.99"
// and every downstream pack-count divides by 144.
//
// Only whole multiples of the factor are pulled back (12 -> 1, 24 -> 2); an
// oddball like 18 is left alone rather than guessed at.
func sanitizeEstimatePack(unit string, packSize float64) (string, float64) {
	per, ok := Convert(1, unit, "each", nil)
	if ok && per > 1 && packSize >= per && math.Mod(packSize, per) == 0 {
		return unit, packSize / per
	}
	return unit, packSize
}

// stripCodeFences strips a leading/trailing ```json or ``` fence. Many models
// wrap "JSON only" responses in a fence anyway; without this every response
// fails to parse and this provider silently returns "no answer" for everything.
func stripCodeFences(s string) string {
	for _, prefix := range []string{"```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			s = strings.TrimPrefix(s, "\n")
			s = strings.TrimSuffix(strings.TrimSpace(s), "```")
			s = strings.TrimSpace(s)
			break
		}
	}
	return s
}
