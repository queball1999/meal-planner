package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"goeat/llm"
)

// fakeBatchGen answers LookupBatch's prompt with a price for ids 0..29,
// enough to cover any chunk this package's batch size produces, and counts
// how many times Generate was called so tests can assert on call count
// (batching, chunking) rather than parsing prompts.
type fakeBatchGen struct{ calls int }

func (g *fakeBatchGen) ProviderName() string { return "fake" }
func (g *fakeBatchGen) ModelName() string    { return "fake-model" }
func (g *fakeBatchGen) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.calls++
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%d,"price_cents":%d,"purchase_unit":"each","pack_size":1}`, i, 100+i)
	}
	sb.WriteString("]")
	return llm.GenerateResponse{Content: sb.String()}, nil
}

// erroringGen always fails, to exercise LookupBatch's retry cap.
type erroringGen struct{ calls int }

func (g *erroringGen) ProviderName() string { return "fake" }
func (g *erroringGen) ModelName() string    { return "fake-model" }
func (g *erroringGen) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.calls++
	return llm.GenerateResponse{}, errors.New("boom")
}

// TestLookupBatch_StopsAfterMaxAttempts is the "don't continuously retry"
// requirement: a chunk that keeps failing is tried aiEstimateMaxAttempts (2)
// times and then left unresolved, not retried indefinitely.
func TestLookupBatch_StopsAfterMaxAttempts(t *testing.T) {
	gen := &erroringGen{}
	ai := NewAIEstimateProvider(gen, "90210")

	results := ai.LookupBatch(context.Background(), []string{"milk", "eggs"}, "")

	if gen.calls != aiEstimateMaxAttempts {
		t.Fatalf("Generate called %d times, want %d (retry cap)", gen.calls, aiEstimateMaxAttempts)
	}
	for i, r := range results {
		if r != nil {
			t.Errorf("results[%d] = %+v, want nil - every attempt failed", i, r)
		}
	}
}

// TestLookupBatch_OneCallForManyTerms is the batching requirement: pricing
// several ingredients must not cost one LLM call per ingredient.
func TestLookupBatch_OneCallForManyTerms(t *testing.T) {
	gen := &fakeBatchGen{}
	ai := NewAIEstimateProvider(gen, "90210")

	terms := []string{"flour", "sugar", "butter", "milk", "eggs"}
	results := ai.LookupBatch(context.Background(), terms, "")

	if gen.calls != 1 {
		t.Fatalf("Generate called %d times, want 1 for %d terms", gen.calls, len(terms))
	}
	for i, r := range results {
		if r == nil || r.PriceCents <= 0 {
			t.Errorf("results[%d] = %+v, want a resolved estimate", i, r)
		}
	}
}

// TestLookupBatch_ChunksLargeLists confirms a list longer than
// aiEstimateBatchSize still resolves fully, split across more than one call.
func TestLookupBatch_ChunksLargeLists(t *testing.T) {
	gen := &fakeBatchGen{}
	ai := NewAIEstimateProvider(gen, "90210")

	terms := make([]string, aiEstimateBatchSize+5)
	for i := range terms {
		terms[i] = fmt.Sprintf("item-%d", i)
	}
	results := ai.LookupBatch(context.Background(), terms, "")

	if gen.calls != 2 {
		t.Fatalf("Generate called %d times, want 2 (%d items chunked at %d per call)", gen.calls, len(terms), aiEstimateBatchSize)
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("results[%d] is nil, want a resolved estimate", i)
		}
	}
}

func TestSanitizeEstimatePack(t *testing.T) {
	cases := []struct {
		name         string
		unit         string
		packSize     float64
		wantUnit     string
		wantPackSize float64
	}{
		// The reported bug: "dozen" + 12 means one carton, not 12 dozen.
		{"dozen twelve -> one", "dozen", 12, "dozen", 1},
		{"dozen twenty-four -> two", "dozen", 24, "dozen", 2},
		{"two dozen pack left alone", "dozen", 2, "dozen", 2},
		{"eighteen-count not guessed", "dozen", 18, "dozen", 18},
		{"plain each untouched", "each", 12, "each", 12},
		{"weight unit untouched", "lb", 5, "lb", 5},
		{"fractional dozen untouched", "dozen", 1.5, "dozen", 1.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, p := sanitizeEstimatePack(c.unit, c.packSize)
			if u != c.wantUnit || p != c.wantPackSize {
				t.Errorf("sanitizeEstimatePack(%q, %v) = (%q, %v), want (%q, %v)",
					c.unit, c.packSize, u, p, c.wantUnit, c.wantPackSize)
			}
		})
	}
}
