package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"goeat/llm"
)

// AIEstimateProvider asks the configured LLM for a typical current price (§6.2).
// Always the floor of the chain — available even with zero integrations.
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
Return ONLY valid JSON — no prose, no markdown:
{"price_cents": <integer cents>, "purchase_unit": "<each|oz|lb|cup|pack>", "pack_size": <float>}`,
		term, r,
	)

	resp, err := a.gen.Generate(ctx, llm.GenerateRequest{
		System:    "You are a grocery pricing assistant. Return only JSON.",
		Prompt:    prompt,
		MaxTokens: 128,
	})
	if err != nil {
		return nil, nil // fail soft
	}

	var out struct {
		PriceCents   int64   `json:"price_cents"`
		PurchaseUnit string  `json:"purchase_unit"`
		PackSize     float64 `json:"pack_size"`
	}
	raw := strings.TrimSpace(resp.Content)
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil // unparseable — fail soft
	}
	if out.PriceCents <= 0 {
		return nil, nil
	}
	if out.PackSize <= 0 {
		out.PackSize = 1
	}

	return &PriceResult{
		PriceCents:   out.PriceCents,
		PurchaseUnit: out.PurchaseUnit,
		PackSize:     out.PackSize,
		Source:       "estimate",
		Confidence:   ConfidenceEstimate,
		FetchedAt:    time.Now().UTC(),
	}, nil
}
