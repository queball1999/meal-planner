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

func (a *AIEstimateProvider) Name() string { return aiEstimateProviderName }

// aiEstimateProviderName is Name()'s return value, referenced by costing.go to
// exclude this provider from the chain when batching (see BatchAIEstimates).
const aiEstimateProviderName = "ai_estimate"

// aiEstimateBatchSize caps how many terms go into one LookupBatch prompt, so a
// long shopping list doesn't produce one oversized request the model
// truncates or fumbles - chunked instead of unbounded.
const aiEstimateBatchSize = 25

// aiEstimateMaxAttempts is how many times one chunk is tried before it is
// left unresolved. Not a general-purpose retry loop: a chunk that fails twice
// stops there rather than being retried again, so a struggling or
// misconfigured provider doesn't get hammered once per chunk indefinitely.
const aiEstimateMaxAttempts = 2

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

// LookupBatch estimates prices for many terms in as few LLM calls as
// possible - one call per aiEstimateBatchSize terms - instead of the one
// call per ingredient Lookup makes. Used by costing.go's ResolvePricing for
// the whole shopping list's chain misses at once, rather than a per-item
// AI-estimate call inside the pricing loop.
//
// Returns a slice parallel to terms: results[i] is terms[i]'s estimate, or
// nil when a chunk failed (after aiEstimateMaxAttempts) or the model omitted
// that term. Matching is by an "id" position echoed back by the model, not by
// the term string, so a reworded or reordered echo still lands correctly.
func (a *AIEstimateProvider) LookupBatch(ctx context.Context, terms []string, region string) []*PriceResult {
	results := make([]*PriceResult, len(terms))
	if a.gen == nil || len(terms) == 0 {
		return results
	}
	r := region
	if r == "" {
		r = a.region
	}

	for start := 0; start < len(terms); start += aiEstimateBatchSize {
		end := start + aiEstimateBatchSize
		if end > len(terms) {
			end = len(terms)
		}
		chunk := terms[start:end]

		chunkResults, err := a.requestBatchWithRetry(ctx, chunk, r)
		if err != nil {
			log.Printf("pricing: ai estimate batch of %d items failed after %d attempts: %v", len(chunk), aiEstimateMaxAttempts, err)
			continue // fail soft - this chunk's terms stay unresolved
		}
		copy(results[start:end], chunkResults)
	}
	return results
}

// requestBatchWithRetry tries one chunk up to aiEstimateMaxAttempts times.
// No unbounded retry against a struggling provider - two failures and this
// chunk is left for the caller's own fallback (§7.3 tier 3).
func (a *AIEstimateProvider) requestBatchWithRetry(ctx context.Context, terms []string, region string) ([]*PriceResult, error) {
	var lastErr error
	for attempt := 1; attempt <= aiEstimateMaxAttempts; attempt++ {
		results, err := a.requestBatch(ctx, terms, region)
		if err == nil {
			return results, nil
		}
		lastErr = err
		log.Printf("pricing: ai estimate batch attempt %d/%d failed: %v", attempt, aiEstimateMaxAttempts, err)
	}
	return nil, lastErr
}

// requestBatch makes one LLM call estimating prices for every term in the
// chunk, matching the response back to terms by position ("id") rather than
// by re-parsing an echoed name.
func (a *AIEstimateProvider) requestBatch(ctx context.Context, terms []string, region string) ([]*PriceResult, error) {
	type reqItem struct {
		ID   int    `json:"id"`
		Term string `json:"term"`
	}
	items := make([]reqItem, len(terms))
	for i, t := range terms {
		items[i] = reqItem{ID: i, Term: t}
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	prompt := fmt.Sprintf(
		`Estimate the typical US grocery store price for each of these items in the region/ZIP: %s.
Items: %s
Return ONLY valid JSON - no prose, no markdown - as an array with one object per item, echoing each item's "id":
[{"id": <id>, "price_cents": <integer cents>, "purchase_unit": "<each|oz|lb|cup|pack>", "pack_size": <float>}]`,
		region, string(itemsJSON),
	)

	// Same no-reasoning, generous-token-budget reasoning as Lookup, scaled to
	// however many items are in this chunk.
	resp, err := a.gen.Generate(ctx, llm.GenerateRequest{
		System:            "You are a grocery pricing assistant. Return only JSON.",
		Prompt:            prompt,
		MaxTokens:         80*len(terms) + 256,
		SuppressReasoning: true,
	})
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	var out []struct {
		ID           int     `json:"id"`
		PriceCents   int64   `json:"price_cents"`
		PurchaseUnit string  `json:"purchase_unit"`
		PackSize     float64 `json:"pack_size"`
	}
	raw := stripCodeFences(strings.TrimSpace(resp.Content))
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("bad response %q: %w", raw, err)
	}

	results := make([]*PriceResult, len(terms))
	for _, o := range out {
		if o.ID < 0 || o.ID >= len(terms) || o.PriceCents <= 0 {
			continue
		}
		packSize := o.PackSize
		if packSize <= 0 {
			packSize = 1
		}
		unit, ps := sanitizeEstimatePack(o.PurchaseUnit, packSize)
		results[o.ID] = &PriceResult{
			PriceCents:   o.PriceCents,
			PurchaseUnit: unit,
			PackSize:     ps,
			Source:       "estimate",
			Confidence:   ConfidenceEstimate,
			FetchedAt:    time.Now().UTC(),
		}
	}
	return results, nil
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
