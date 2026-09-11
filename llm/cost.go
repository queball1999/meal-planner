package llm

import (
	"goeat/db"
)

// CostEstimate is one model's usage joined to its reference price. Priced is
// false when the model has no mapping - it is reported as "unpriced", never as
// $0.00, so a missing price can't be mistaken for a free one.
type CostEstimate struct {
	Model            string
	Provider         string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	RunCount         int
	EstimatedCents   int64
	Priced           bool
	ReferenceModel   string // the reference model the price came from, when priced
}

// EstimateCostUSD computes the reference cost of a token volume against a
// per-million price. Pure arithmetic, no DB.
func EstimateCostUSD(promptTokens, completionTokens, inputPerMillion, outputPerMillion float64) float64 {
	const perMillion = 1_000_000.0
	return (float64(promptTokens)/perMillion)*inputPerMillion +
		(float64(completionTokens)/perMillion)*outputPerMillion
}

// EstimateCosts joins a set of per-model usage rows to their reference prices.
// Unmapped models come back with Priced=false (not dropped), so the dashboard
// can show them as "unpriced" and prompt for a mapping.
func EstimateCosts(usage []*db.AIRunByModel, pricing map[string]*db.PricingReference) []*CostEstimate {
	out := make([]*CostEstimate, 0, len(usage))
	for _, u := range usage {
		ce := &CostEstimate{
			Model:            u.Model,
			Provider:         u.Provider,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
			RunCount:         u.RunCount,
		}
		if ref, ok := pricing[u.Model]; ok && ref != nil {
			ce.Priced = true
			ce.ReferenceModel = ref.ReferenceModel
			ce.EstimatedCents = int64(EstimateCostUSD(
				float64(u.PromptTokens), float64(u.CompletionTokens),
				ref.InputPricePerMillion, ref.OutputPricePerMillion,
			) * 100)
		}
		out = append(out, ce)
	}
	return out
}

// SummariseCosts totals a set of cost estimates. EstimatedCents sums only the
// priced rows; UnpricedCount is how many models still need a mapping, so the
// total reads as an honest lower bound.
func SummariseCosts(estimates []*CostEstimate) (estimatedCents int64, unpricedCount int) {
	for _, e := range estimates {
		if e.Priced {
			estimatedCents += e.EstimatedCents
		} else {
			unpricedCount++
		}
	}
	return estimatedCents, unpricedCount
}
